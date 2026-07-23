package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func readNetstring(buf []byte) ([]byte, bool) {
	i := bytes.IndexByte(buf, ':')
	if i < 0 {
		return nil, false
	}
	// Reject non-digit characters (including leading '+' or '-')
	for _, c := range buf[:i] {
		if c < '0' || c > '9' {
			return nil, false
		}
	}
	n, err := strconv.Atoi(string(buf[:i]))
	if err != nil || n < 0 {
		return nil, false
	}
	// Sanity cap: reject lengths > 1<<24
	if n > 1<<24 {
		return nil, false
	}
	start := i + 1
	end := start + n
	if len(buf) < end+1 || buf[end] != ',' {
		return nil, false
	}
	// Reject trailing data after the netstring's closing comma
	if len(buf) > end+1 {
		return nil, false
	}
	return buf[start:end], true
}

// readNetstringStream reads ONE djb netstring (<len>:<bytes>,) from a stream (unlike
// readNetstring, which parses a single fully-buffered frame). ok=false on EOF or malformed
// framing (non-digit length, short body, missing comma, length > 1<<24). Used by the v1 path,
// which is a SEQUENCE of netstrings terminated by the empty netstring 0:,.
func readNetstringStream(r *bufio.Reader) ([]byte, bool) {
	// Read the length token digit-by-digit with a HARD cap. This must be bounded BEFORE the
	// value is trusted: bufio.ReadString(':') buffers the whole pre-':' run unbounded, so an
	// endless stream of digits with no ':' drives allocation to OOM (a confined caller measured
	// ~1 GB RSS). A valid length is <= 1<<24 = 16777216 (8 digits), so >8 digits is malformed.
	const maxLenDigits = 8
	var lenBuf []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, false // EOF before ':' — truncated
		}
		if b == ':' {
			break
		}
		if b < '0' || b > '9' {
			return nil, false
		}
		lenBuf = append(lenBuf, b)
		if len(lenBuf) > maxLenDigits {
			return nil, false
		}
	}
	if len(lenBuf) == 0 {
		return nil, false
	}
	n, err := strconv.Atoi(string(lenBuf))
	if err != nil || n > 1<<24 {
		return nil, false
	}
	content := make([]byte, n)
	if _, err := io.ReadFull(r, content); err != nil {
		return nil, false
	}
	comma, err := r.ReadByte()
	if err != nil || comma != ',' {
		return nil, false
	}
	return content, true
}

// parseV2Content splits a v2 netstring's content into argv (a run of netstrings terminated by
// the empty netstring 0:,) and the trailing payload. Length-framed argv preserves argument
// boundaries and lets any byte (space, NUL, 0xFF) appear in an argument; readNetstringStream
// bounds each element and its length token, so a hostile content cannot OOM here.
func parseV2Content(content []byte) (argv [][]byte, payload []byte, ok bool) {
	const maxArgs = 4096
	const maxFrameBytes = 1 << 20
	r := bufio.NewReader(bytes.NewReader(content))
	// First netstring = argc (decimal). An explicit count makes the empty string a REPRESENTABLE
	// argument: with an empty-netstring terminator, a legit "" arg was indistinguishable from the
	// end of argv and silently truncated the command, bleeding the tail into stdin (reviewer 964
	// #1). Reading exactly argc elements removes the collision and bounds the count (#2).
	argcB, good := readNetstringStream(r)
	if !good {
		return nil, nil, false
	}
	argc, err := strconv.Atoi(string(argcB))
	if err != nil || argc < 0 || argc > maxArgs {
		return nil, nil, false
	}
	total := 0
	argv = make([][]byte, 0, argc)
	for i := 0; i < argc; i++ {
		ns, good := readNetstringStream(r)
		if !good {
			return nil, nil, false
		}
		total += len(ns)
		if total > maxFrameBytes {
			return nil, nil, false
		}
		argv = append(argv, ns) // fresh slice per element; empty "" is a valid arg now
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, false
	}
	return argv, rest, true
}

func waitForChild(cmd *exec.Cmd) int {
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "sicd: wait: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	signal.Ignore(syscall.SIGPIPE)

	// Dual-read dispatch on the FIRST byte, so a v2 daemon can be deployed without bricking the
	// v1 clients still in the field: 0x00 = the v2 preamble; an ASCII digit = the first byte of
	// a v1 netstring length (legacy, no preamble); anything else is malformed.
	first := make([]byte, 1)
	if _, err := io.ReadFull(os.Stdin, first); err != nil {
		fmt.Fprintf(os.Stderr, "sicd: read first byte: %v\n", err)
		os.Exit(1)
	}
	switch {
	case first[0] == 0x00:
		runV2() // magic consumed; the rest (len32 + netstring + trailing) is on stdin
	case first[0] >= '0' && first[0] <= '9':
		// The byte we consumed for dispatch is the first digit of the first netstring's
		// length — push it back before parsing.
		runV1(bufio.NewReader(io.MultiReader(bytes.NewReader(first), os.Stdin)))
	default:
		fmt.Fprintf(os.Stderr, "sicd: invalid first byte: 0x%02x\n", first[0])
		os.Exit(1)
	}
}

// runV2 is the v2 wire path (magic already consumed by main): 4-byte big-endian length, one
// netstring whose content is <command> 0x00 <payload>, then trailing stdin forwarded to the child.
func runV2() {
	lenBytes := make([]byte, 4)
	if _, err := io.ReadFull(os.Stdin, lenBytes); err != nil {
		fmt.Fprintf(os.Stderr, "sicd: read length: %v\n", err)
		os.Exit(1)
	}
	nsLen := binary.BigEndian.Uint32(lenBytes)
	if nsLen > 1<<24 {
		// reviewer #3: cap BEFORE make — a raw uint32 (up to 4 GiB) was allocated before
		// readNetstring's cap ever ran (5-byte input committed ~4 GB).
		fmt.Fprintf(os.Stderr, "sicd: netstring length %d exceeds cap\n", nsLen)
		os.Exit(1)
	}

	nsBuf := make([]byte, nsLen)
	if _, err := io.ReadFull(os.Stdin, nsBuf); err != nil {
		fmt.Fprintf(os.Stderr, "sicd: read netstring: %v\n", err)
		os.Exit(1)
	}

	content, ok := readNetstring(nsBuf)
	if !ok {
		fmt.Fprintf(os.Stderr, "sicd: invalid netstring\n")
		os.Exit(1)
	}

	// v2 content = netstring(argc) + argc argv netstrings + payload. Length-framed argv PRESERVES
	// argument boundaries (sic's founding guarantee: `touch 'a b'` is ONE file) and lets ANY byte
	// appear in an argument — unlike the old space-split. The explicit argc (not an empty-netstring
	// terminator) makes an empty "" argument representable (reviewer 964 #1); everything after the
	// argc argv netstrings is the payload (a nested frame for the next hop, or empty).
	argv, payload, ok := parseV2Content(content)
	if !ok {
		fmt.Fprintf(os.Stderr, "sicd: invalid v2 content (argv framing)\n")
		os.Exit(1)
	}
	if len(argv) == 0 {
		fmt.Fprintf(os.Stderr, "sicd: empty command\n")
		os.Exit(1)
	}
	argvStr := make([]string, len(argv))
	for i, a := range argv {
		argvStr[i] = string(a)
	}

	cmd := exec.Command(argvStr[0], argvStr[1:]...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sicd: create stdin pipe: %v\n", err)
		os.Exit(1)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sicd: exec %s: %v\n", argvStr[0], err)
		os.Exit(1)
	}

	if err := writeAll(stdinPipe, payload); err != nil {
		if errors.Is(err, syscall.EPIPE) {
			// Child died before reading all payload.
		} else {
			fmt.Fprintf(os.Stderr, "sicd: write payload: %v\n", err)
		}
		stdinPipe.Close()
		os.Exit(waitForChild(cmd))
	}

	os.Exit(pumpStdin(stdinPipe, os.Stdin, func() int { return waitForChild(cmd) }, os.Stderr))
}

// runV1 handles the legacy v1 wire the deployed cmd/sic still sends (raw netstrings, no
// preamble): first netstring = mode ("exec" | "sh"); then the argv/command netstrings;
// terminated by the empty netstring 0:,. Everything after the terminator is the child's stdin.
// Kept only for the migration window so a v2 daemon does not reject v1 callers.
func runV1(r *bufio.Reader) {
	modeB, ok := readNetstringStream(r)
	if !ok {
		fmt.Fprintf(os.Stderr, "sicd: v1 malformed frame (mode)\n")
		os.Exit(1)
	}
	mode := string(modeB)
	if mode != "exec" && mode != "sh" {
		fmt.Fprintf(os.Stderr, "sicd: v1 unknown mode %q\n", mode)
		os.Exit(1)
	}

	// Read argv/command netstrings until the empty-netstring terminator. EOF first = truncated.
	// Cap the frame (element count AND total bytes): readNetstringStream bounds each netstring at
	// 1<<24, but nothing bounds their NUMBER — 2M tiny netstrings measured ~140 MB before exec.
	// NOTE: an empty netstring 0:, is ALWAYS the terminator, so a legitimate empty "" argument
	// collides with it — the arg list truncates there and the rest bleeds into the child's stdin.
	// That is inherent to the v1 wire (terminator overload), not a bug introduced here; the
	// deployed cmd/sic produces it only if a caller passes a literal "".
	const maxArgs = 4096
	const maxFrameBytes = 1 << 20
	var argv []string
	total := 0
	for {
		ns, ok := readNetstringStream(r)
		if !ok {
			fmt.Fprintf(os.Stderr, "sicd: v1 truncated frame (no 0:, terminator)\n")
			os.Exit(1)
		}
		if len(ns) == 0 {
			break // the empty netstring 0:, terminates the frame
		}
		total += len(ns)
		if len(argv) >= maxArgs || total > maxFrameBytes {
			fmt.Fprintf(os.Stderr, "sicd: v1 frame too large\n")
			os.Exit(1)
		}
		argv = append(argv, string(ns))
	}

	var cmd *exec.Cmd
	switch mode {
	case "exec":
		if len(argv) == 0 {
			fmt.Fprintf(os.Stderr, "sicd: v1 empty exec argv\n")
			os.Exit(1)
		}
		cmd = exec.Command(argv[0], argv[1:]...) // each netstring is ONE argv element, never space-split
	case "sh":
		cmd = exec.Command("sh", "-c", strings.Join(argv, " "))
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sicd: create stdin pipe: %v\n", err)
		os.Exit(1)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "sicd: exec: %v\n", err)
		os.Exit(1)
	}

	// v1 has no framed payload: everything after 0:, (still buffered in r + the rest of stdin)
	// is the child's stdin. Reuse the v2 pump so EPIPE/reap/exit-status semantics match exactly.
	os.Exit(pumpStdin(stdinPipe, r, func() int { return waitForChild(cmd) }, os.Stderr))
}

// pumpStdin copies src (sicd's own stdin) into the child's stdin (dst), closes dst so the
// child sees EOF, then reaps. EPIPE from the WRITE side means the child stopped reading
// (| head semantics) — not an error, so the child's own status is returned. Any OTHER error
// (a genuine read/write failure) means the transfer was truncated: report a diagnostic, reap
// to avoid a zombie, and return non-zero REGARDLESS of the child's status — a truncated
// transfer must never be reported as success. Extracted so a mock erroring io.Reader can
// cover this branch: that is exactly how the Python reference tests it (monkeypatching
// os.read to raise OSError), since no real fd on this platform yields a read error on stdin
// (a pty slave and a socket peer both see a clean kernel EOF on hangup, in Go AND CPython).
func pumpStdin(dst io.WriteCloser, src io.Reader, reap func() int, errOut io.Writer) int {
	_, err := io.Copy(dst, src)
	dst.Close()
	if err != nil && !errors.Is(err, syscall.EPIPE) {
		fmt.Fprintf(errOut, "sicd: forward stdin: %v\n", err)
		reap()
		return 1
	}
	return reap()
}

func writeAll(w io.Writer, data []byte) error {
	for off := 0; off < len(data); {
		n, err := w.Write(data[off:])
		if err != nil {
			return err
		}
		off += n
	}
	return nil
}
