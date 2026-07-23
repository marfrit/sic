package main

import "testing"

// WP5: verbFor maps a hop's runtime TYPE + container name to the runtime verb that enters it.
// The inner-hop command is this verb with " sicd" appended (wrapHop does the append), so the
// verb must end right where "sicd" should follow: incus/pct need the "--" argv separator,
// docker exec does not. Unknown runtime is a hard error (fail-loud, never guess a verb).
func TestVerbFor(t *testing.T) {
	cases := []struct {
		runtime, name, want string
	}{
		{"incus", "app", "incus exec app --"},
		{"docker", "db", "docker exec db"},
		{"pct", "107", "pct exec 107 --"},
	}
	for _, tc := range cases {
		got, err := verbFor(tc.runtime, tc.name)
		if err != nil {
			t.Fatalf("verbFor(%q,%q) unexpected err: %v", tc.runtime, tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("verbFor(%q,%q) = %q, want %q", tc.runtime, tc.name, got, tc.want)
		}
	}
	if _, err := verbFor("bogus", "x"); err == nil {
		t.Fatal("verbFor with an unknown runtime must return an error, not guess a verb")
	}
}
