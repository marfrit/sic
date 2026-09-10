package main

import (
	"fmt"
	"strings"
)

// shellMeta are the bytes that only mean anything to a shell. A single argv
// element carrying one of them is almost always a whole command line that the
// caller's outer quotes collapsed into one word.
const shellMeta = " \t\n|&;<>()$`\"'"

// looksLikeShellLine reports whether rest is a SINGLE argument carrying shell
// syntax — the shape of `sic host 'prog arg | other'`.
//
// sic execs argv directly and never starts a shell (see the package comment),
// so such an element becomes a PROGRAM NAME that contains spaces. exec then
// fails with "no such file or directory" naming the entire line, which reads
// like an unreachable host or a missing tool and sends the reader looking in
// the wrong place entirely. Observed 2026-09-10: a 400-character lmcp-tool
// call was reported as "hertz's lmcp refuses writes" when nothing was wrong
// with hertz at all.
//
// Only the len==1 case qualifies. `sic host touch 'a b'` is TWO elements and
// is precisely what sic exists to preserve; it must never be rejected.
func looksLikeShellLine(rest []string) bool {
	return len(rest) == 1 && strings.ContainsAny(rest[0], shellMeta)
}

// shellLineHint names the three ways out. It shows only the head of the
// offending argument: that it was far too long to read is the whole problem.
func shellLineHint(arg string) string {
	shown := arg
	if len(shown) > 60 {
		shown = shown[:60] + "…"
	}
	return fmt.Sprintf(`%q is ONE argument, not a program name.

sic execs argv directly and never starts a shell, so a whole command line
quoted into a single word cannot work. Pick one:

  argv        sic <host> lmcp-tool mneme 'command=recall a b'
  shell line  sic --sh <host> 'prog a | prog b'
  script      sic <host> python3 - < script.py`, shown)
}
