package main

import (
	"encoding/binary"
	"strconv"
)

// v2Frame returns one v2 wire frame:
//
//	0x00 ++ big-endian uint32 length of the netstring ++ netstring
//
// where netstring = "<len>:<content>," and content = command ++ 0x00 ++ payload.
func v2Frame(command []byte, payload []byte) []byte {
	// Build content: command + 0x00 + payload
	content := make([]byte, len(command)+1+len(payload))
	copy(content, command)
	content[len(command)] = 0x00
	copy(content[len(command)+1:], payload)

	// Build netstring: "<len>:<content>,"
	netstr := strconv.Itoa(len(content)) + ":" + string(content) + ","

	// Build frame: 0x00 ++ be32(len(netstr)) ++ netstr
	frame := make([]byte, 1+4+len(netstr))
	frame[0] = 0x00
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(netstr)))
	copy(frame[5:], netstr)

	return frame
}

// wrapHop wraps an already-built inner frame for one chain hop.
func wrapHop(verb string, inner []byte) []byte {
	return v2Frame([]byte(verb+" sicd"), inner)
}

// buildChain builds a chain of wrapped frames from verbs, with the innermost being v2Frame(cmd, payload).
func buildChain(verbs []string, cmd []byte, payload []byte) []byte {
	frame := v2Frame(cmd, payload)
	for i := len(verbs) - 1; i >= 0; i-- {
		frame = wrapHop(verbs[i], frame)
	}
	return frame
}
