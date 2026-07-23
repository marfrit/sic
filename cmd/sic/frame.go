package main

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// nsBytes builds a djb netstring "<len>:<bytes>," over arbitrary bytes (binary-safe).
func nsBytes(b []byte) []byte {
	out := append([]byte(strconv.Itoa(len(b))), ':')
	out = append(out, b...)
	return append(out, ',')
}

// v2Frame builds one v2 wire frame from an EXPLICIT argv (boundary-preserving) plus a payload:
//
//	0x00 ++ big-endian uint32 length of the netstring ++ netstring(content)
//	content = netstring(argc) ++ <argv netstrings> ++ payload
//
// Length-framed argv is why `sic host touch 'a b'` sends ONE argument, not two — no space-split
// anywhere. The payload is the next hop's nested frame, or empty for the innermost command.
func v2Frame(argv [][]byte, payload []byte) []byte {
	// content = netstring(argc) + argv netstrings + payload. The explicit count (not an empty-
	// netstring terminator) makes an empty "" argument representable (reviewer 964 #1).
	content := nsBytes([]byte(strconv.Itoa(len(argv))))
	for _, a := range argv {
		content = append(content, nsBytes(a)...)
	}
	content = append(content, payload...)

	ns := nsBytes(content)
	frame := make([]byte, 1+4+len(ns))
	frame[0] = 0x00
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(ns)))
	copy(frame[5:], ns)
	return frame
}

// wrapHop wraps an inner frame for one chain hop: the hop's argv is the runtime verb split on
// spaces with "sicd" appended (the inner-hop peeler), and the inner frame is the payload. Verbs
// are space-safe by construction (no spaces in container names/ids), so splitting them is fine.
func wrapHop(verb string, inner []byte) []byte {
	fields := strings.Fields(verb)
	argv := make([][]byte, 0, len(fields)+1)
	for _, f := range fields {
		argv = append(argv, []byte(f))
	}
	argv = append(argv, []byte("sicd"))
	return v2Frame(argv, inner)
}

// buildChain builds the whole onion: innermost v2Frame(cmdArgv, payload), then wrap outward so
// verbs[0] is the outermost layer (the one the host's sicd enters first). Zero verbs yields a
// single v2 frame (a bare host with no nesting).
func buildChain(verbs []string, cmdArgv [][]byte, payload []byte) []byte {
	frame := v2Frame(cmdArgv, payload)
	for i := len(verbs) - 1; i >= 0; i-- {
		frame = wrapHop(verbs[i], frame)
	}
	return frame
}
