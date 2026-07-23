// dualread_test.go — executable spec for daemon dual-read (wire protocol v1 + v2).
//
// The DEPLOYED cmd/sic client speaks v1: raw netstrings with NO preamble —
//
//	v1 frame := netstring(mode) argnets "0:,"
//	mode     := "exec" | "sh"
//	argnets  := netstring(arg)...          (exec: one netstring PER argv element)
//	          | netstring(joined command)  (sh: single space-joined command line)
//
// Bytes after the "0:," terminator are the child's stdin, 8-bit clean.
// Semantics: exec runs argv directly (NO shell, NO space-splitting — each
// netstring is one argv element); sh runs the command line through a shell;
// the daemon inherits the child's exit status.
//
// The current daemon speaks only v2 (0x00 magic + be32 length + netstring)
// and exits 1 on anything else, which bricks every deployed v1 client.
// Dual-read contract: dispatch on the FIRST byte — ASCII digit ⇒ v1 frame,
// 0x00 ⇒ v2 frame, anything else ⇒ exit 1 with a diagnostic, exec nothing.
//
// Reuses the sicd_test.go harness (same package): TestMain/sicdBin, runSicd,
// frame, netstring, concat, shScript, pattern.
//
// NOTE for the implementer: sicd_test.go's malformed-input case
// "old-v1-frame-without-preamble" asserts the OPPOSITE of this contract and
// must be deleted when dual-read lands — both cannot be green.
package main

import (
	"bytes"
	"strings"
	"testing"
)

// v1Frame replicates cmd/sic frameArgv byte-for-byte: mode netstring, then
// args as individual netstrings ("exec") or one space-joined netstring
// ("sh"), then the empty-netstring terminator.
func v1Frame(mode string, args ...string) []byte {
	var b bytes.Buffer
	b.Write(netstring([]byte(mode)))
	switch mode {
	case "exec":
		for _, a := range args {
			b.Write(netstring([]byte(a)))
		}
	case "sh":
		b.Write(netstring([]byte(strings.Join(args, " "))))
	}
	b.WriteString("0:,")
	return b.Bytes()
}

// --- v1 acceptance (RED on the current daemon: dies at the magic-byte check) --

func TestV1ExecFrameRunsCommand(t *testing.T) {
	r := runSicd(t, v1Frame("exec", "echo", "hello"))
	if r.code != 0 {
		t.Fatalf("v1 exec frame must run; expected exit 0, got %d (killedBy=%v), stderr=%q",
			r.code, r.killedBy, r.stderr)
	}
	if got := string(r.stdout); got != "hello\n" {
		t.Fatalf("stdout = %q, want %q", got, "hello\n")
	}
}

func TestV1ExecArgvBoundariesPreserved(t *testing.T) {
	// v1 exec carries each argument in its OWN netstring: an arg containing
	// a space is one argv element. A daemon that funnels v1 through the v2
	// space-split path hands the script two args and prints "2|a".
	script := shScript(t, `printf '%s|%s\n' "$#" "$1"`)
	r := runSicd(t, v1Frame("exec", string(script), "a b"))
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d, stderr=%q", r.code, r.stderr)
	}
	if got, want := string(r.stdout), "1|a b\n"; got != want {
		t.Fatalf("argv boundaries lost: script saw %q, want %q — v1 args must NOT be space-split", got, want)
	}
}

func TestV1ExecTrailingStdinForwardedToChild(t *testing.T) {
	// Bytes after the "0:," terminator are the child's stdin, verbatim:
	// 1 KiB covering every byte value (NULs, 0xFF, netstring-ish colons and
	// commas) must never be re-parsed as framing or mangled.
	body := pattern(4)
	r := runSicd(t, concat(v1Frame("exec", "cat"), body))
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (killedBy=%v), stderr=%q", r.code, r.killedBy, r.stderr)
	}
	if !bytes.Equal(r.stdout, body) {
		t.Fatalf("trailing stdin truncated or corrupted: got %d of %d bytes", len(r.stdout), len(body))
	}
}

func TestV1ShModeRunsThroughShell(t *testing.T) {
	// sh mode is the one v1 path that DOES go through a shell: a pipeline
	// must actually pipe. `echo hi | wc -c` ⇒ "3".
	r := runSicd(t, v1Frame("sh", "echo hi | wc -c"))
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d, stderr=%q", r.code, r.stderr)
	}
	if got := strings.TrimSpace(string(r.stdout)); got != "3" {
		t.Fatalf("pipeline did not run through a shell: stdout = %q, want \"3\"", got)
	}
}

func TestV1ChildExitStatusInherited(t *testing.T) {
	r := runSicd(t, v1Frame("sh", "exit 7"))
	if r.code != 7 {
		t.Fatalf("v1 must inherit the child's exit status 7, got %d, stderr=%q", r.code, r.stderr)
	}
}

// --- v2 must keep working alongside v1 (green today; regression guard) -------

func TestV2FrameStillAcceptedAlongsideV1(t *testing.T) {
	payload := []byte("v2 payload\n")
	trailing := []byte("and trailing stdin")
	r := runSicd(t, concat(frame([]byte("cat"), payload), trailing))
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (killedBy=%v), stderr=%q", r.code, r.killedBy, r.stderr)
	}
	if want := concat(payload, trailing); !bytes.Equal(r.stdout, want) {
		t.Fatalf("stdout = %q, want %q", r.stdout, want)
	}
}

// --- dispatch must stay strict (green today; pins the future dispatcher) -----

func TestDispatchRejectsUnknownFirstByte(t *testing.T) {
	// Dual-read must not degenerate into "anything non-0x00 is v1": a first
	// byte that is neither an ASCII digit nor 0x00 exits 1, execs nothing.
	r := runSicd(t, concat([]byte{0xff}, v1Frame("exec", "cat")))
	if r.code != 1 {
		t.Fatalf("expected exit 1, got %d (killedBy=%v)", r.code, r.killedBy)
	}
	if len(r.stdout) != 0 {
		t.Fatalf("unknown first byte must not exec anything, stdout=%q", r.stdout)
	}
	if len(bytes.TrimSpace(r.stderr)) == 0 {
		t.Fatal("unknown first byte must produce a diagnostic on stderr")
	}
}

func TestV1TruncatedFrameMissingTerminatorExits1(t *testing.T) {
	// EOF before the "0:," terminator: malformed — diagnose and exit 1,
	// never hang waiting for more netstrings and never exec a partial argv.
	r := runSicd(t, concat(netstring([]byte("exec")), netstring([]byte("cat"))))
	if r.code != 1 {
		t.Fatalf("expected exit 1, got %d (killedBy=%v), stdout=%q", r.code, r.killedBy, r.stdout)
	}
	if len(r.stdout) != 0 {
		t.Fatalf("truncated v1 frame must not exec anything, stdout=%q", r.stdout)
	}
	if len(bytes.TrimSpace(r.stderr)) == 0 {
		t.Fatal("truncated v1 frame must produce a diagnostic on stderr")
	}
}

func TestV1UnknownModeRejected(t *testing.T) {
	// Mode is a closed set {exec, sh}; anything else is malformed, not a
	// command to run.
	wire := concat(netstring([]byte("rm")), netstring([]byte("-rf")), []byte("0:,"))
	r := runSicd(t, wire)
	if r.code != 1 {
		t.Fatalf("expected exit 1, got %d (killedBy=%v), stdout=%q", r.code, r.killedBy, r.stdout)
	}
	if len(bytes.TrimSpace(r.stderr)) == 0 {
		t.Fatal("unknown v1 mode must produce a diagnostic on stderr")
	}
}

func TestV1EmptyExecArgvRejected(t *testing.T) {
	// "4:exec,0:," — terminator immediately after the mode: no command.
	r := runSicd(t, v1Frame("exec"))
	if r.code != 1 {
		t.Fatalf("expected exit 1, got %d (killedBy=%v), stdout=%q", r.code, r.killedBy, r.stdout)
	}
	if len(bytes.TrimSpace(r.stderr)) == 0 {
		t.Fatal("empty v1 exec argv must produce a diagnostic on stderr")
	}
}
