package main

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"testing"
)

// Independent reference builders (a second implementation, so a bug in frame.go can't cancel out
// of both sides of the assertion).
func refNS(b []byte) []byte {
	out := []byte(strconv.Itoa(len(b)) + ":")
	out = append(out, b...)
	return append(out, ',')
}
func refFrame(content []byte) []byte {
	ns := refNS(content)
	f := []byte{0x00}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ns)))
	return append(append(f, l[:]...), ns...)
}
func refContent(argv [][]byte, payload []byte) []byte {
	var c []byte
	for _, a := range argv {
		c = append(c, refNS(a)...)
	}
	c = append(c, "0:,"...)
	return append(c, payload...)
}

func TestV2Frame(t *testing.T) {
	// An argument containing a space is ONE length-framed element — sic's founding guarantee.
	argv := [][]byte{[]byte("cat"), []byte("a b")}
	payload := []byte("hi")
	want := refFrame(refContent(argv, payload))
	if got := v2Frame(argv, payload); !bytes.Equal(got, want) {
		t.Fatalf("v2Frame =\n % x\nwant\n % x", got, want)
	}
}

func TestV2FrameBinaryAndEmpty(t *testing.T) {
	// Payload bytes that look like framing ride untouched; nil and []byte{} payload are identical.
	argv := [][]byte{[]byte("x")}
	payload := []byte{0x00, 0xFF, ',', ':'}
	if got := v2Frame(argv, payload); !bytes.Equal(got, refFrame(refContent(argv, payload))) {
		t.Fatalf("binary payload frame mismatch")
	}
	if !bytes.Equal(v2Frame(argv, nil), v2Frame(argv, []byte{})) {
		t.Fatal("nil and empty payload must frame identically")
	}
}

func TestWrapHop(t *testing.T) {
	inner := v2Frame([][]byte{[]byte("cat")}, nil)
	// verb split on spaces, then "sicd" appended
	want := v2Frame([][]byte{[]byte("incus"), []byte("exec"), []byte("c"), []byte("--"), []byte("sicd")}, inner)
	if got := wrapHop("incus exec c --", inner); !bytes.Equal(got, want) {
		t.Fatalf("wrapHop mismatch")
	}
}

func TestBuildChain(t *testing.T) {
	cmd := [][]byte{[]byte("cat")}
	payload := []byte("hi")
	if got, want := buildChain(nil, cmd, payload), v2Frame(cmd, payload); !bytes.Equal(got, want) {
		t.Fatal("zero hops must equal a single v2 frame")
	}
	verbs := []string{"incus exec c --", "docker exec d"}
	want := wrapHop(verbs[0], wrapHop(verbs[1], v2Frame(cmd, payload)))
	if got := buildChain(verbs, cmd, payload); !bytes.Equal(got, want) {
		t.Fatal("buildChain must fold verbs[0] as the outermost layer")
	}
}
