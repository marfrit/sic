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

	content, _, ok := readNetstring(nsBuf)
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

	// Pump remaining stdin to child. EPIPE on the write side means the
	// child stopped reading (| head semantics) — that is not an error.
	// Any other error (EIO, etc.) means sicd's own stdin failed mid-stream
	// and the transfer was truncated — exit non-zero with a diagnostic.
	if _, err := io.Copy(stdinPipe, os.Stdin); err != nil && !errors.Is(err, syscall.EPIPE) {
		fmt.Fprintf(os.Stderr, "sicd: forward stdin: %v\n", err)
		stdinPipe.Close()
		os.Exit(1)
	}
	stdinPipe.Close()

	os.Exit(waitForChild(cmd))
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
