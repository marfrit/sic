package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
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

	magic := make([]byte, 1)
	if _, err := io.ReadFull(os.Stdin, magic); err != nil {
		fmt.Fprintf(os.Stderr, "sicd: read magic: %v\n", err)
		os.Exit(1)
	}
	if magic[0] != 0x00 {
		fmt.Fprintf(os.Stderr, "sicd: invalid magic byte: 0x%02x\n", magic[0])
		os.Exit(1)
	}

	lenBytes := make([]byte, 4)
	if _, err := io.ReadFull(os.Stdin, lenBytes); err != nil {
		fmt.Fprintf(os.Stderr, "sicd: read length: %v\n", err)
		os.Exit(1)
	}
	nsLen := binary.BigEndian.Uint32(lenBytes)

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

	nulIdx := bytes.IndexByte(content, 0x00)
	if nulIdx < 0 {
		fmt.Fprintf(os.Stderr, "sicd: content missing NUL separator\n")
		os.Exit(1)
	}
	command := content[:nulIdx]
	payload := content[nulIdx+1:]

	argv := bytes.Split(command, []byte(" "))
	argvStr := make([]string, len(argv))
	for i, a := range argv {
		argvStr[i] = string(a)
	}
	if len(argvStr) == 0 {
		fmt.Fprintf(os.Stderr, "sicd: empty command\n")
		os.Exit(1)
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
