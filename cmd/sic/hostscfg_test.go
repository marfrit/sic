package main

import (
	"reflect"
	"testing"
)

// WP7: parseHostsConfig parses the minimal hosts.toml subset (no external dep — stdlib only):
//   # comment
//   [<host>]
//   nest = ["incus", "docker"]
// into map[host] -> ordered runtime types. Comments and blank lines are ignored; an empty nest
// list is a present host with zero hops.
func TestParseHostsConfig(t *testing.T) {
	data := "# fleet nest config\n" +
		"[boltzmann]\n" +
		"nest = [\"incus\", \"docker\"]\n" +
		"\n" +
		"[data]\n" +
		"nest = [\"pct\"]\n" +
		"\n" +
		"[bosch]\n" +
		"nest = []\n"
	cfg, err := parseHostsConfig(data)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !reflect.DeepEqual(cfg["boltzmann"], []string{"incus", "docker"}) {
		t.Fatalf("boltzmann nest = %#v, want [incus docker]", cfg["boltzmann"])
	}
	if !reflect.DeepEqual(cfg["data"], []string{"pct"}) {
		t.Fatalf("data nest = %#v, want [pct]", cfg["data"])
	}
	b, ok := cfg["bosch"]
	if !ok || len(b) != 0 {
		t.Fatalf("bosch must be present with an empty nest; got %#v ok=%v", b, ok)
	}
}
