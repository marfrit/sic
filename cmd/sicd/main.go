// sicd — receive netstring-framed argv over stdin, execvp it directly.
//
// Deployed on target hosts. Reads netstrings from stdin, interprets the first
// field as a mode selector ("exec" or "sh"), and runs the command accordingly.
//
// Wire format (netstrings, djb):
//
//	<mode-netstring> <arg-netstring>* <empty-netstring>
//
// Usage:
//
//	ssh <host> sicd < /some/netstring/frames
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// readNetstring reads one netstring from buf. Returns (payload, rest, ok).
func readNetstring(buf []byte) ([]byte, []byte, bool) {
	i := bytes.IndexByte(buf, ':')
	if i < 0 {
		return nil, nil, false
	}
	n, err := strconv.Atoi(string(buf[:i]))
	if err != nil || n < 0 {
		return nil, nil, false
	}
	start := i + 1
	end := start + n
	if len(buf) < end+1 || buf[end] != ',' {
		return nil, nil, false
	}
	return buf[start:end], buf[end+1:], true
}

// runFrame executes a single command frame.
func runFrame(mode string, args [][]byte) int {
	switch mode {
	case "exec":
		argv := make([]string, len(args))
		for i, a := range args {
			argv[i] = string(a)
		}
		if len(argv) == 0 {
			fmt.Fprintln(os.Stderr, "sicd: exec mode with no args")
			return 1
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			fmt.Fprintf(os.Stderr, "sicd: %v\n", err)
			return 1
		}
		return 0
	case "sh":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "sicd: sh mode with no args")
			return 1
		}
		shellCmd := string(args[0])
		cmd := exec.Command("sh", "-c", shellCmd)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode()
			}
			fmt.Fprintf(os.Stderr, "sicd: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "sicd: unknown mode %q\n", mode)
		return 1
	}
}

func main() {
	buf, err := os.ReadFile("/dev/stdin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "sicd: read stdin: %v\n", err)
		os.Exit(1)
	}

	var mode string
	var fields [][]byte
	framesProcessed := 0

	for {
		field, rest, ok := readNetstring(buf)
		if !ok {
			break
		}
		buf = rest

		if mode == "" {
			mode = string(field)
		} else if len(field) == 0 {
			// Empty netstring = end of frame
			if mode != "" {
				runFrame(mode, fields)
				framesProcessed++
			}
			mode = ""
			fields = fields[:0]
		} else {
			fields = append(fields, field)
		}
	}

	if framesProcessed == 0 {
		os.Exit(1)
	}
}
