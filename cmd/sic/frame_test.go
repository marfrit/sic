package main

import (
	"bytes"
	"testing"
)

// WP1 spec for v2Frame: one v2 wire frame, byte-exact to what cmd/sicd parses:
//   0x00  +  big-endian uint32 length of the netstring  +  netstring(command 0x00 payload)
// Hand-written byte literals (no helper arithmetic that could cancel a framer bug out of both
// sides). Byte counts verified against cmd/sicd/main.go's readNetstring, which rejects an
// over- or under-counted length, so these lengths must be exact.

func TestV2FrameCatHi(t *testing.T) {
	// content = "cat\x00hi" (6 bytes); netstring "6:cat\x00hi," (9 bytes); frame 14 bytes.
	want := []byte{
		0x00,                   // magic
		0x00, 0x00, 0x00, 0x09, // be32(9) = length of the netstring
		'6', ':', 'c', 'a', 't', 0x00, 'h', 'i', ',',
	}
	if got := v2Frame([]byte("cat"), []byte("hi")); !bytes.Equal(got, want) {
		t.Fatalf("v2Frame(cat,hi) =\n % x\nwant\n % x", got, want)
	}
}

func TestV2FrameEmptyPayload(t *testing.T) {
	// content = "x\x00" (2 bytes); netstring "2:x\x00," (5 bytes); frame 10 bytes.
	want := []byte{
		0x00,
		0x00, 0x00, 0x00, 0x05, // be32(5)
		'2', ':', 'x', 0x00, ',',
	}
	if got := v2Frame([]byte("x"), nil); !bytes.Equal(got, want) {
		t.Fatalf("v2Frame(x,nil) =\n % x\nwant\n % x", got, want)
	}
	if got := v2Frame([]byte("x"), []byte{}); !bytes.Equal(got, want) {
		t.Fatalf("nil and []byte{} payload must frame identically; got\n % x\nwant\n % x", got, want)
	}
}

func TestV2FrameBinaryPayloadRidesUntouched(t *testing.T) {
	// Payload bytes that look like framing (NUL, 0xFF, comma, colon) must ride INSIDE the
	// netstring untouched — it is length-delimited, never re-parsed by delimiter.
	payload := []byte{0x00, 0xFF, ',', ':'}
	// content = "cat" + 0x00 + payload = 8 bytes; netstring "8:...," = 11 bytes; frame 16 bytes.
	want := []byte{
		0x00,
		0x00, 0x00, 0x00, 0x0B, // be32(11)
		'8', ':', 'c', 'a', 't', 0x00, 0x00, 0xFF, ',', ':', ',',
	}
	if got := v2Frame([]byte("cat"), payload); !bytes.Equal(got, want) {
		t.Fatalf("v2Frame binary payload =\n % x\nwant\n % x", got, want)
	}
}
