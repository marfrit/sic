// sicd_test.go — executable spec for the sic wire protocol v2, ported from
// tests/test_sicd_protocol.py (Python reference daemon: gateway/sicd).
//
// Wire format (one hop):
//
//	frame     := MAGIC len32 netstring
//	MAGIC     := 0x00                     (1 byte)
//	len32     := big-endian uint32        (byte length of the netstring)
//	netstring := <n>:<content>,           (djb netstring)
//	content   := <command> 0x00 <payload> (split at the FIRST NUL; NUL required)
//
// Semantics under test: each hop peels exactly ONE layer; the payload is
// opaque bytes; the child's stdin is payload ++ every remaining byte of
// sicd's own stdin; malformed input exits 1 with a diagnostic and execs
// nothing; sicd inherits the child's exit status, signal death as
// 128+WTERMSIG.
//
// EPIPE note: CPython sets SIGPIPE to SIG_IGN at startup, so the reference
// sees BrokenPipeError. The Go runtime only shields writes to fds 1 and 2;
// on the child pipe the implementation must handle syscall.EPIPE explicitly
// or the EPIPE path becomes a SIGPIPE death / unhandled error.
//
// Black-box: TestMain builds the sicd binary once; every test drives it as a
// subprocess with a 10s hang watchdog.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const timeout = 10 * time.Second

var sicdBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sicd-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tempdir: %v\n", err)
		os.Exit(1)
	}
	sicdBin = filepath.Join(dir, "sicd")
	if out, err := exec.Command("go", "build", "-o", sicdBin, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building sicd: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- wire builders -----------------------------------------------------------

func netstring(data []byte) []byte {
	return concat([]byte(strconv.Itoa(len(data))+":"), data, []byte{','})
}

func be32(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}

// frame builds one wire frame: magic, 4-byte BE length, netstring(command NUL payload).
func frame(command, payload []byte) []byte {
	ns := netstring(concat(command, []byte{0x00}, payload))
	return concat([]byte{0x00}, be32(uint32(len(ns))), ns)
}

func concat(parts ...[]byte) []byte {
	var b bytes.Buffer
	for _, p := range parts {
		b.Write(p)
	}
	return b.Bytes()
}

// pattern returns n repetitions of the 256-byte sequence 0x00..0xFF.
func pattern(n int) []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	return bytes.Repeat(b, n)
}

// shScript writes an executable /bin/sh script and returns its path as a
// command (no spaces in the path, so it survives the argv space-split).
func shScript(t *testing.T, body string) []byte {
	t.Helper()
	script := filepath.Join(t.TempDir(), "cmd.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return []byte(script)
}

// --- subprocess driver -------------------------------------------------------

type result struct {
	code     int
	stdout   []byte
	stderr   []byte
	killedBy syscall.Signal // signal that killed sicd ITSELF, 0 if none
}

func runSicd(t *testing.T, wire []byte) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sicdBin)
	cmd.Stdin = bytes.NewReader(wire)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	waitErr := cmd.Run()
	return finish(t, ctx, cmd, waitErr, &out, &errb)
}

// runSicdFeed streams stdin to sicd through a pipe under the caller's control.
func runSicdFeed(t *testing.T, feed func(w io.Writer)) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sicdBin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sicd: %v", err)
	}
	feed(stdin)
	stdin.Close()
	waitErr := cmd.Wait()
	return finish(t, ctx, cmd, waitErr, &out, &errb)
}

func finish(t *testing.T, ctx context.Context, cmd *exec.Cmd, waitErr error, out, errb *bytes.Buffer) result {
	t.Helper()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("sicd hung instead of completing (stdin not consumed/forwarded correctly); stderr so far: %q", errb.String())
	}
	if cmd.ProcessState == nil {
		t.Fatalf("sicd did not run: %v", waitErr)
	}
	res := result{stdout: out.Bytes(), stderr: errb.Bytes()}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.killedBy = ws.Signal()
		res.code = -1
	} else {
		res.code = cmd.ProcessState.ExitCode()
	}
	return res
}

// --- happy path: peeling, forwarding, streaming ------------------------------

func TestFramePeeling(t *testing.T) {
	trailing := []byte("then \x00 raw \xff bytes, 7:not,a netstring\n")
	body64k := pattern(256)  // 65,536 bytes, every byte value
	body64k1 := pattern(257) // 65,792 bytes > 64 KiB pipe buffer
	bodyMiB := pattern(4096) // 1 MiB
	tests := []struct {
		name string
		wire []byte
		want []byte
	}{
		{
			// One frame, command=cat: cat's stdin is exactly the payload.
			"single-hop-payload-reaches-command-stdin",
			frame([]byte("cat"), []byte("hello from hop zero\n")),
			[]byte("hello from hop zero\n"),
		},
		{
			// Bytes after the frame reach the child's stdin after the
			// payload, verbatim: raw NULs, 0xFF, and netstring-ish
			// colon/comma material must never be re-parsed as framing.
			"trailing-stdin-forwarded-after-payload",
			concat(frame([]byte("cat"), []byte("payload-first:")), trailing),
			concat([]byte("payload-first:"), trailing),
		},
		{
			// The historical zero-byte-file bug: sicd read ALL of stdin for
			// framing, so a streamed body after the frame silently vanished.
			"zero-byte-file-regression-64k-body-not-swallowed",
			concat(frame([]byte("cat"), nil), body64k),
			body64k,
		},
		{
			// Empty payload, no trailing bytes: immediate EOF, no hang.
			"empty-payload-command-gets-immediate-eof",
			frame([]byte("cat"), nil),
			nil,
		},
		{
			// Body exceeding the 64 KiB pipe buffer must not deadlock: the
			// parent must pump through the pipe after fork, not pre-write
			// the whole blob and block.
			"large-payload-64k-plus-1-no-deadlock",
			concat(frame([]byte("cat"), nil), body64k1),
			body64k1,
		},
		{
			// 1 MiB pins the streaming pump loop, not just the buffer edge.
			"multi-mib-body-no-deadlock",
			concat(frame([]byte("cat"), nil), bodyMiB),
			bodyMiB,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runSicd(t, tc.wire)
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (killedBy=%v), stderr=%q", r.code, r.killedBy, r.stderr)
			}
			if !bytes.Equal(r.stdout, tc.want) {
				t.Fatalf("stdout mismatch: got %d bytes, want %d bytes", len(r.stdout), len(tc.want))
			}
		})
	}
}

func TestTwoHopNestedPeel(t *testing.T) {
	// The outer payload is itself a complete frame (starting with the 0x00
	// magic byte) for an inner sicd: each hop peels exactly ONE layer and
	// never looks inside the payload. Because the inner frame is full of
	// NULs, this also pins "split at the FIRST NUL" — a split at any later
	// NUL corrupts the hop.
	inner := frame([]byte("cat"), []byte("nested payload survived two hops\n"))
	outer := frame([]byte(sicdBin), inner)
	r := runSicd(t, outer)
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (killedBy=%v), stderr=%q", r.code, r.killedBy, r.stderr)
	}
	if got, want := string(r.stdout), "nested payload survived two hops\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestCommandSplitOnSpacesNoShell(t *testing.T) {
	// argv = command split on ASCII spaces, exec'd directly (shell=False):
	// shell metacharacters in the command are plain argv bytes.
	r := runSicd(t, frame([]byte("echo hello $HOME ;id"), nil))
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d, stderr=%q", r.code, r.stderr)
	}
	if got, want := string(r.stdout), "hello $HOME ;id\n"; got != want {
		t.Fatalf("stdout = %q, want %q — command must not pass through a shell", got, want)
	}
}

func TestBareDoubleDashPayloadIsOpaque(t *testing.T) {
	// A bare -- inside the payload is data, never an option/separator: it
	// must reach the command's stdin byte-for-byte and never abort the run.
	for _, payload := range [][]byte{
		[]byte("--"),
		[]byte("-- --help --version\n"),
		[]byte("a -- b\n"),
	} {
		t.Run(strconv.Quote(string(payload)), func(t *testing.T) {
			r := runSicd(t, frame([]byte("cat"), payload))
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d, stderr=%q", r.code, r.stderr)
			}
			if !bytes.Equal(r.stdout, payload) {
				t.Fatalf("stdout = %q, want %q", r.stdout, payload)
			}
		})
	}
}

// --- malformed input ---------------------------------------------------------

func TestMalformedInputExits1WithDiagnostic(t *testing.T) {
	tests := []struct {
		name string
		wire []byte
	}{
		{"empty-input", nil},
		{"missing-magic", []byte("garbage with no magic byte")},
		{"old-v1-frame-without-preamble", []byte("4:exec,3:cat,0:,")},
		{"body-not-a-netstring", concat([]byte{0x00}, be32(14), []byte("not&a netstrng"))},
		{"truncated-declared-length", concat([]byte{0x00}, be32(100), netstring([]byte("cat\x00hi")))},
		{"content-missing-nul-truncated", concat([]byte{0x00}, be32(6), netstring([]byte("ca")))},
		{"content-missing-nul-separator", concat([]byte{0x00}, be32(5), netstring([]byte("ca")))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runSicd(t, tc.wire)
			if r.code != 1 {
				t.Fatalf("expected exit 1, got %d (killedBy=%v)", r.code, r.killedBy)
			}
			if len(r.stdout) != 0 {
				t.Fatalf("malformed input must not silently exec anything, stdout=%q", r.stdout)
			}
			if len(bytes.TrimSpace(r.stderr)) == 0 {
				t.Fatal("malformed input must produce a diagnostic on stderr")
			}
		})
	}
}

func TestNonexistentCommandExitsNonzero(t *testing.T) {
	r := runSicd(t, frame([]byte("__nonexistent_command_42__"), nil))
	if r.killedBy != 0 {
		t.Fatalf("sicd itself was killed by %v", r.killedBy)
	}
	if r.code == 0 {
		t.Fatal("exec of a nonexistent command must not hang or exit 0")
	}
}

// --- exit-status fidelity ----------------------------------------------------

func TestChildExitStatusInherited(t *testing.T) {
	r := runSicd(t, frame(shScript(t, "exit 7"), nil))
	if r.code != 7 {
		t.Fatalf("expected sicd to inherit child exit status 7, got %d, stderr=%q", r.code, r.stderr)
	}
}

func TestSignalKilledChildExits128PlusTermsig(t *testing.T) {
	// exit 1 is indistinguishable from an ordinary command failure; signal
	// death must surface as 128+WTERMSIG (shell convention) so the caller
	// can tell a crashed/killed command from one that merely returned false.
	tests := []struct {
		name string
		sig  string
		want int
	}{
		{"SIGKILL-137", "KILL", 137},
		{"SIGTERM-143", "TERM", 143},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runSicd(t, frame(shScript(t, "kill -"+tc.sig+" $$"), nil))
			if r.code != tc.want {
				t.Fatalf("child died from SIG%s; expected exit %d (128+WTERMSIG), got %d (killedBy=%v, stderr=%q)",
					tc.sig, tc.want, r.code, r.killedBy, r.stderr)
			}
		})
	}
}

// --- fault injection: EPIPE, short reads, stdin read error -------------------
//
// The Python suite monkeypatches os.fork/os.write/os.read; a compiled Go
// binary cannot be patched, so each fault is reproduced with real kernel
// behaviour instead:
//
//	widowed pipe   : the child dies without reading stdin while the payload
//	                 (256 KiB) is far larger than the 64 KiB pipe buffer, so
//	                 the parent is guaranteed to still be writing when the
//	                 pipe is widowed — deterministic EPIPE, no signal races.
//	short reads    : the whole wire arrives in 7-byte chunks over a pipe, so
//	                 framing reads and the pump loop see partial reads.
//	                 (Go's runtime already loops partial WRITES internally,
//	                 so the Python short-write fault maps to the read side.)
//	stdin read err : sicd's stdin is a raw pty slave; closing the master
//	                 mid-stream makes the next read fail with EIO, exactly
//	                 how a dying tty / revoked fd fails.

func TestEpipeChildDeadBeforePayloadWriteIsHandled(t *testing.T) {
	// The child is a failed exec: dead before it ever reads. The 256 KiB
	// payload cannot fit the pipe buffer, so the payload write MUST hit
	// EPIPE. sicd must handle it (Go: check for syscall.EPIPE — there is no
	// CPython-style BrokenPipeError safety net on non-std fds), report the
	// exec failure, and exit with the child's status 1 — never crash.
	payload := pattern(1024) // 256 KiB
	r := runSicd(t, frame([]byte("__nonexistent_command_42__"), payload))
	if r.killedBy != 0 {
		t.Fatalf("sicd itself was killed by %v — EPIPE/SIGPIPE on the child pipe is not handled", r.killedBy)
	}
	if bytes.Contains(r.stderr, []byte("panic")) {
		t.Fatalf("unhandled panic instead of clean EPIPE handling:\n%s", r.stderr)
	}
	if r.code != 1 {
		t.Fatalf("expected the child's exit status 1, got %d, stderr=%q", r.code, r.stderr)
	}
	if !bytes.Contains(r.stderr, []byte("__nonexistent_command_42__")) {
		t.Fatalf("stderr must name the command that failed to exec, got %q", r.stderr)
	}
}

func TestSignalKilledChildEpipePathExits137(t *testing.T) {
	// Same 128+WTERMSIG contract on the EPIPE recovery path: the child
	// SIGKILLs itself without reading stdin, and the 256 KiB payload
	// guarantees the parent is still writing into a widowed pipe when it
	// dies. The status inherited there must also be 137, not 1.
	r := runSicd(t, frame(shScript(t, "kill -KILL $$"), pattern(1024)))
	if r.killedBy != 0 {
		t.Fatalf("sicd itself was killed by %v — EPIPE/SIGPIPE on the child pipe is not handled", r.killedBy)
	}
	if bytes.Contains(r.stderr, []byte("panic")) {
		t.Fatalf("unhandled panic on the EPIPE path:\n%s", r.stderr)
	}
	if r.code != 137 {
		t.Fatalf("expected exit 137 (128+SIGKILL) via the EPIPE path, got %d, stderr=%q", r.code, r.stderr)
	}
}

func TestFramingAndPumpSurviveTrickledStdin(t *testing.T) {
	// Short-read torture: preamble, netstring, and an 8 KiB trailing body
	// arrive in 7-byte chunks. Framing must loop its reads to exactness
	// (a single os.Read of 5 bytes is not guaranteed 5 bytes) and the pump
	// loop must forward every partial read without loss or reordering.
	body := pattern(32) // 8 KiB
	wire := concat(frame([]byte("cat"), nil), body)
	r := runSicdFeed(t, func(w io.Writer) {
		for off := 0; off < len(wire); off += 7 {
			end := min(off+7, len(wire))
			if _, err := w.Write(wire[off:end]); err != nil {
				return // sicd died early; Wait() reports it
			}
			if off%(7*128) == 0 {
				time.Sleep(time.Millisecond) // force fragmentation on the pipe
			}
		}
	})
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (killedBy=%v), stderr=%q", r.code, r.killedBy, r.stderr)
	}
	if !bytes.Equal(r.stdout, body) {
		t.Fatalf("trickled stdin truncated or corrupted: got %d of %d bytes", len(r.stdout), len(body))
	}
}

func TestStdinReadErrorDiagnosedNotSwallowed(t *testing.T) {
	// An OSError reading sicd's OWN stdin (EIO here; EBADF, ENXIO likewise)
	// is not `| head` semantics — only EPIPE on the write side is. Silently
	// swallowing it reports truncation as success. Required: non-zero exit
	// and a handled diagnostic on stderr (not a panic).
	master, slave := openPty(t)
	defer master.Close()
	defer slave.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sicdBin)
	cmd.Stdin = slave
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sicd: %v", err)
	}
	slave.Close() // the child holds its own copy of the slave fd

	payload := []byte("payload written before the fault\n")
	trailing := bytes.Repeat([]byte("x"), 512)
	if _, err := master.Write(concat(frame([]byte("cat"), payload), trailing)); err != nil {
		t.Fatalf("writing to pty master: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let sicd consume the frame and enter the pump loop
	master.Close()                     // slave reads now fail with EIO

	waitErr := cmd.Wait()
	r := finish(t, ctx, cmd, waitErr, &out, &errb)
	if r.code == 0 {
		t.Fatal("stdin read failed mid-stream (EIO) but sicd exited 0 — silent truncation reported as success")
	}
	if len(bytes.TrimSpace(r.stderr)) == 0 {
		t.Fatal("stdin read failed mid-stream but no diagnostic was written to stderr")
	}
	if bytes.Contains(r.stderr, []byte("panic")) {
		t.Fatalf("want a handled diagnostic, not a panic:\n%s", r.stderr)
	}
}

// --- pty plumbing (Linux) ----------------------------------------------------

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// openPty opens a pty pair with the slave in raw mode so frame bytes (NULs,
// 0xFF, control chars) pass through 8-bit clean. Closing the master later
// makes slave reads fail with EIO.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	var ptn uint32
	if err := ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&ptn)); err != nil {
		t.Fatalf("TIOCGPTN: %v", err)
	}
	var unlock int32
	if err := ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		t.Fatalf("TIOCSPTLCK: %v", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", ptn), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open pty slave: %v", err)
	}
	var tio syscall.Termios
	if err := ioctl(slave.Fd(), syscall.TCGETS, unsafe.Pointer(&tio)); err != nil {
		t.Fatalf("TCGETS: %v", err)
	}
	tio.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	tio.Oflag &^= syscall.OPOST
	tio.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	tio.Cflag &^= syscall.CSIZE | syscall.PARENB
	tio.Cflag |= syscall.CS8
	tio.Cc[syscall.VMIN] = 1
	tio.Cc[syscall.VTIME] = 0
	if err := ioctl(slave.Fd(), syscall.TCSETS, unsafe.Pointer(&tio)); err != nil {
		t.Fatalf("TCSETS: %v", err)
	}
	return master, slave
}
