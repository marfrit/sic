package main

import (
	"reflect"
	"testing"
)

// WP7: parseHostsConfig parses the minimal hosts.toml subset (no external dep — stdlib only):
//
//	# comment
//	[<host>]
//	nest = ["incus", "docker"]
//
// into map[host] -> ordered runtime types. Comments and blank lines are ignored; an empty nest
// list is a present host with zero hops.
func TestParseHostsConfig(t *testing.T) {
	data := "# fleet nest config\n" +
		"[host1]\n" +
		"nest = [\"incus\", \"docker\"]\n" +
		"\n" +
		"[host2]\n" +
		"nest = [\"pct\"]\n" +
		"\n" +
		"[host3]\n" +
		"nest = []\n"
	cfg, err := parseHostsConfig(data)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !reflect.DeepEqual(cfg["host1"], []string{"incus", "docker"}) {
		t.Fatalf("host1 nest = %#v, want [incus docker]", cfg["host1"])
	}
	if !reflect.DeepEqual(cfg["host2"], []string{"pct"}) {
		t.Fatalf("host2 nest = %#v, want [pct]", cfg["host2"])
	}
	b, ok := cfg["host3"]
	if !ok || len(b) != 0 {
		t.Fatalf("host3 must be present with an empty nest; got %#v ok=%v", b, ok)
	}
}
