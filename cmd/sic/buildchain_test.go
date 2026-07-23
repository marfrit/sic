package main

import (
	"bytes"
	"testing"
)

// WP3: buildChain folds wrapHop over the resolved per-hop verbs (verbs[0] outermost) around the
// innermost v2Frame(cmd, payload). Relational: each want is built from the real v2Frame/wrapHop,
// so this pins the composition and fold ORDER, not a re-derived byte layout.
func TestBuildChain(t *testing.T) {
	cmd := []byte("cat")
	payload := []byte("hi")
	cases := []struct {
		name  string
		verbs []string
		want  []byte
	}{
		{"zero hops nil = single v2 frame", nil, v2Frame(cmd, payload)},
		{"zero hops empty slice", []string{}, v2Frame(cmd, payload)},
		{"one hop", []string{"incus exec c --"},
			wrapHop("incus exec c --", v2Frame(cmd, payload))},
		{"two hops, verbs[0] is the OUTERMOST layer", []string{"incus exec c --", "docker exec d"},
			wrapHop("incus exec c --", wrapHop("docker exec d", v2Frame(cmd, payload)))},
	}
	for _, tc := range cases {
		if got := buildChain(tc.verbs, cmd, payload); !bytes.Equal(got, tc.want) {
			t.Fatalf("%s:\n got  % x\n want % x", tc.name, got, tc.want)
		}
	}
}
