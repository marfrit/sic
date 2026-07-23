package main

import (
	"reflect"
	"testing"
)

// WP4: parseTarget splits "host/hop1/hop2" into the ssh host + the ordered nested hops. A bare
// host (no '/') has no hops. Pure split; validation (empty host, hop count vs nest depth) is
// resolveVerbs' job, not this one.
func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		host string
		hops []string
	}{
		{"host1", "host1", nil},
		{"host1/app", "host1", []string{"app"}},
		{"host1/app/db", "host1", []string{"app", "db"}},
	}
	for _, tc := range cases {
		host, hops := parseTarget(tc.in)
		if host != tc.host || !reflect.DeepEqual(hops, tc.hops) {
			t.Fatalf("parseTarget(%q) = (%q, %#v), want (%q, %#v)", tc.in, host, hops, tc.host, tc.hops)
		}
	}
}
