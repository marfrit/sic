// Pins wrapHop, the one-hop chain wrapper the client does not have yet:
//
//	func wrapHop(verb string, inner []byte) []byte
//
// It wraps an already-built inner frame for ONE chain hop: the hop's command
// is the runtime verb with " sicd" appended (the inner-hop peeler is sicd
// itself), and the inner frame rides as the payload. Exactly:
//
//	wrapHop(verb, inner) == v2Frame([]byte(verb+" sicd"), inner)
//
// The equivalence tests pin that identity against v2Frame (frame.go, WP1);
// the concrete tests are hand-written byte literals, so a shared framer bug
// cannot cancel out of both sides.
package main

import (
	"bytes"
	"testing"
)

func TestWrapHopEqualsV2FrameOfVerbSicd(t *testing.T) {
	// nestedInner is a real v2 frame (v2Frame("x", nil)): starts with the
	// 0x00 magic, contains NULs — must ride untouched as payload.
	nestedInner := []byte{0x00, 0x00, 0x00, 0x00, 0x05, '2', ':', 'x', 0x00, ','}
	cases := []struct {
		name  string
		verb  string
		inner []byte
	}{
		{"simple", "cat", []byte("hi")},
		{"incus verb, nested frame inner", "incus exec memory --", nestedInner},
		{"empty inner", "sh -c", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapHop(tc.verb, tc.inner)
			want := v2Frame([]byte(tc.verb+" sicd"), tc.inner)
			if !bytes.Equal(got, want) {
				t.Fatalf("wrapHop(%q, %q)\n got %q\nwant %q", tc.verb, tc.inner, got, want)
			}
		})
	}
}

func TestWrapHopConcreteBytesNestedInnerUntouched(t *testing.T) {
	// inner is itself a full v2 frame beginning with the 0x00 magic — a real
	// nested frame. It must appear verbatim as the payload: no delimiter
	// parsing, no escaping, no truncation at its leading NUL.
	inner := []byte{0x00, 0x00, 0x00, 0x00, 0x05, '2', ':', 'x', 0x00, ','}
	// command = "incus exec memory -- sicd" (25 bytes)
	// content = command + 0x00 + inner = 25 + 1 + 10 = 36 bytes
	// netstring = "36:" + content + "," = 40 bytes -> frame = 1 + 4 + 40 = 45
	want := []byte{
		0x00, // magic
		0x00, 0x00, 0x00, 0x28, // be32 netstring length (40)
		'3', '6', ':',
		'i', 'n', 'c', 'u', 's', ' ', 'e', 'x', 'e', 'c', ' ',
		'm', 'e', 'm', 'o', 'r', 'y', ' ', '-', '-', ' ', 's', 'i', 'c', 'd',
		0x00,
		0x00, 0x00, 0x00, 0x00, 0x05, '2', ':', 'x', 0x00, ',',
		',',
	}
	got := wrapHop("incus exec memory --", inner)
	if len(got) != 45 {
		t.Fatalf("frame length = %d, want 45 (magic 1 + len32 4 + netstring 40): %q", len(got), got)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wrapHop(incus exec memory --, nested)\n got %q\nwant %q", got, want)
	}
}

func TestWrapHopEmptyInner(t *testing.T) {
	// command = "cat sicd" (8) -> content = command + 0x00 = 9 bytes
	// -> netstring "9:<content>," = 12 bytes -> frame = 17 bytes
	want := []byte{
		0x00,
		0x00, 0x00, 0x00, 0x0c,
		'9', ':', 'c', 'a', 't', ' ', 's', 'i', 'c', 'd', 0x00, ',',
	}
	if got := wrapHop("cat", nil); !bytes.Equal(got, want) {
		t.Fatalf("wrapHop(cat, nil)\n got %q\nwant %q", got, want)
	}
	// nil and empty inner frame identically.
	if got := wrapHop("cat", []byte{}); !bytes.Equal(got, want) {
		t.Fatalf("wrapHop(cat, []byte{})\n got %q\nwant %q", got, want)
	}
}
