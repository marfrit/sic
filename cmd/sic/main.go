// sic — frame argv as netstrings and pipe to sicd on a remote host.
//
// Each argument is framed as a netstring and written to sicd's stdin over ssh.
// The netstring framing is done here so callers pass argv on the command line
// and never construct frames by hand.
//
// Usage:
//
//	sic [--sh] <host> <command> [<arg> ...]
//	sic [--sh] <host> -- <command> [<arg> ...]   (explicit separator)
//
// Examples:
//
//	sic host1 echo hello world
//	sic host1 touch 'a b'         # ONE file, not two
//	sic --sh host1 'echo hi | wc -c'
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// netstring encodes a UTF-8 string as a netstring (djb format).
func netstring(s string) string {
	encoded := []byte(s)
	return strconv.Itoa(len(encoded)) + ":" + string(encoded) + ","
}

// frameArgv builds a single netstring command frame from args.
func frameArgv(args []string, mode string) []byte {
	var sb strings.Builder
	sb.WriteString(netstring(mode))
	switch mode {
	case "exec":
		for _, a := range args {
			sb.WriteString(netstring(a))
		}
	case "sh":
		sb.WriteString(netstring(strings.Join(args, " ")))
	}
	sb.WriteString("0:,")
	return []byte(sb.String())
}

func main() {
	argv := os.Args[1:]

	// Parse flags
	mode := "exec"
	for len(argv) > 0 && strings.HasPrefix(argv[0], "--") {
		switch argv[0] {
		case "--sh":
			mode = "sh"
		case "--":
			argv = argv[1:]
			goto parseArgs
		}
		argv = argv[1:]
	}

parseArgs:
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: sic [--sh] <host> <command> [<arg> ...]")
		os.Exit(1)
	}

	// Locate host and command
	var host string
	var command []string

	if idx := indexOf(argv, "--"); idx >= 0 {
		host = argv[0]
		command = argv[idx+1:]
	} else if len(argv) >= 2 {
		host = argv[0]
		command = argv[1:]
	} else {
		fmt.Fprintln(os.Stderr, "sic: missing command arguments")
		os.Exit(1)
	}

	frameData := frameArgv(command, mode)
	sshCmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", host, "sicd")
	sshCmd.Stdin = strings.NewReader(string(frameData))
	sshCmd.Stdout = os.Stdout
	sshCmd.Stderr = os.Stderr
	if err := sshCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "sic: %v\n", err)
		os.Exit(1)
	}
}

func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}
