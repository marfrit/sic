package main

import "testing"

// A whole command line quoted into ONE argument is the shape that produced
// `sicd: exec: fork/exec <400 characters>: no such file or directory` in the
// field. sic execs argv directly, so that single element becomes a program
// name. Catch it in the client, where the user's intent is still visible.
func TestLooksLikeShellLine(t *testing.T) {
	for _, c := range []struct {
		name string
		rest []string
		want bool
	}{
		{"whole line in one arg", []string{`lmcp-tool mneme command=remember title="x y"`}, true},
		{"one arg with a pipe", []string{`ls | wc -c`}, true},
		{"one arg with a redirect", []string{`cat >out`}, true},
		{"one arg with a semicolon", []string{`a;b`}, true},
		{"plain program, no args", []string{"uptime"}, false},
		{"proper argv, space inside an ARGUMENT", []string{"touch", "a b"}, false},
		{"proper argv, several", []string{"lmcp-tool", "mneme", "command=recall x y"}, false},
		{"empty", nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeShellLine(c.rest); got != c.want {
				t.Fatalf("looksLikeShellLine(%q) = %v, want %v", c.rest, got, c.want)
			}
		})
	}
}

// The hint must name the three ways out and must not echo a 400-character
// line back at the user.
func TestShellLineHintIsActionableAndBounded(t *testing.T) {
	long := ""
	for i := 0; i < 50; i++ {
		long += "lmcp-tool mneme command=remember "
	}
	h := shellLineHint(long)
	for _, want := range []string{"--sh", "argv", "python3 - <"} {
		if !contains(h, want) {
			t.Fatalf("hint does not mention %q:\n%s", want, h)
		}
	}
	if len(h) > 600 {
		t.Fatalf("hint is %d bytes, too long to read", len(h))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
