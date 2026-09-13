// sic — preserve argv boundaries across ssh (and nested container hops) by framing
// the command as length-delimited netstrings and piping it to sicd on the far end.
//
// The whole point: `sic host touch 'a b'` creates ONE file, never two. No argument is
// ever space-joined or re-split on the way to the remote exec (the one exception is
// --sh, where the user explicitly asked for a shell line).
//
// Usage:
//
//	sic [--sh | --kill] <target> [--] <command> [<arg> ...]
//
// A target is a host, optionally followed by container hops separated by '/':
//
//	sic host echo hi                       # run on the host
//	sic host touch 'a b'                   # ONE file
//	sic host1/app cat /etc/os-release       # into the 'app' guest on host1
//	sic host2/ct/svc id                     # host -> pct ct -> docker svc (two hops)
//	sic --sh host 'echo hi | wc -c'        # shell line (space-join is intended here)
//	sic --kill host llama-server           # list matches, never touching our own tree
//	sic --kill host llama-server --force   # and signal them
//
// Hop runtimes come from /etc/sic/hosts.toml, one stanza per host:
//
//	[host1]
//	nest = ["incus"]
//	[host2]
//	nest = ["pct", "docker"]
package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// sickill runs on the far end as `python3 -`, so it needs no installation there.
// It exists because `pkill -f <pattern>` cannot avoid matching the caller: the
// caller's own command line contains the pattern, because the caller just typed
// it. Over ssh that kills this very session (exit 255); over an MCP shell tool it
// kills the `sh -c '(...)'` wrapper and its forks (measured: 1 self-hit over ssh,
// 3 over lmcp, 2026-09-13).
//
//go:embed sickill.py
var sickillPy []byte

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "sic: "+format+"\n", a...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: sic [--sh | --kill] <target> [--] <command> [<arg> ...]")
	fmt.Fprintln(os.Stderr, "  --kill <target> <pattern> [-x] [--force] [-s SIGNAL]  kill by pattern, never your own tree")
	os.Exit(1)
}

// loadNest returns the runtime nest for a host from /etc/sic/hosts.toml. Only called when
// the target actually has hops — a bare host never needs the config file.
func loadNest(host string) ([]string, error) {
	const path = "/etc/sic/hosts.toml"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("target has hops but %s is unreadable: %w", path, err)
	}
	cfg, err := parseHostsConfig(string(data))
	if err != nil {
		return nil, err
	}
	nest, ok := cfg[host]
	if !ok {
		return nil, fmt.Errorf("no [%s] stanza in %s (needed to resolve hops)", host, path)
	}
	return nest, nil
}

func main() {
	args := os.Args[1:]

	// Leading flags. Everything after the target is the command.
	shMode, killMode := false, false
	for len(args) > 0 {
		if args[0] == "--sh" {
			shMode = true
			args = args[1:]
			continue
		}
		if args[0] == "--kill" {
			killMode = true
			args = args[1:]
			continue
		}
		break
	}
	if shMode && killMode {
		fatal("--sh and --kill are mutually exclusive")
	}
	if len(args) < 1 {
		usage()
	}

	target := args[0]
	rest := args[1:]
	// Optional explicit separator so a command whose first token looks like a flag survives.
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		fatal("missing command")
	}
	// A whole command line quoted into one word can only fail, and it fails in a
	// way that blames the far end. Say so here, where the intent is still visible.
	// Not in --kill mode: there `rest` is a pattern, and a pattern is allowed to
	// look like anything at all.
	if !shMode && !killMode && looksLikeShellLine(rest) {
		fatal("%s", shellLineHint(rest[0]))
	}

	// Build the command argv, boundary-preserved. --sh is the sole place a space-join is
	// correct: the user asked for a shell line, not an argv.
	var cmdArgv [][]byte
	if killMode {
		// The far end runs the embedded script from stdin; the pattern and any
		// flags travel as argv, so they keep their boundaries like everything else.
		cmdArgv = [][]byte{[]byte("python3"), []byte("-")}
		for _, a := range rest {
			cmdArgv = append(cmdArgv, []byte(a))
		}
	} else if shMode {
		cmdArgv = [][]byte{[]byte("sh"), []byte("-c"), []byte(strings.Join(rest, " "))}
	} else {
		cmdArgv = make([][]byte, len(rest))
		for i, a := range rest {
			cmdArgv[i] = []byte(a)
		}
	}

	host, hops := parseTarget(target)

	var verbs []string
	if len(hops) > 0 {
		nest, err := loadNest(host)
		if err != nil {
			fatal("%v", err)
		}
		verbs, err = resolveVerbs(hops, nest)
		if err != nil {
			fatal("%v", err)
		}
	}

	frame := buildChain(verbs, cmdArgv, nil)

	// ssh -T: never a pty — the frame is binary and a pty would mangle it (CR/LF, echo).
	cmd := exec.Command("ssh", "-T", "-o", "StrictHostKeyChecking=no", host, "sicd")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fatal("stdin pipe: %v", err)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatal("ssh: %v", err)
	}

	// Write the onion, THEN forward our own stdin (the v1 client set Stdin to the frame alone
	// and dropped everything after — so `sic host 'cat >f' <local` wrote a zero-byte file).
	// The copy runs in a goroutine we abandon on exit: a command that ignores stdin
	// (`sic host echo hi` on a tty) must not block on a tty read that never EOFs.
	if _, err := stdin.Write(frame); err != nil {
		fatal("write frame: %v", err)
	}
	if killMode {
		// The script IS the far end's stdin -- our own stdin stays untouched, so
		// `sic --kill` works from a terminal without blocking on a read that never EOFs.
		if _, err := stdin.Write(sickillPy); err != nil {
			fatal("write sickill: %v", err)
		}
		stdin.Close()
	} else {
		go func() {
			io.Copy(stdin, os.Stdin)
			stdin.Close()
		}()
	}

	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fatal("ssh: %v", err)
	}
}
