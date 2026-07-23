package main

import (
	"reflect"
	"testing"
)

// WP6: resolveVerbs maps each hop to its verb via verbFor, keyed by DEPTH in the host's nest.
// Too many hops for the declared nest depth is a hard error (fail-loud); an unknown runtime
// propagates verbFor's error. Zero hops -> zero verbs (a bare host is a single v2 frame).
func TestResolveVerbs(t *testing.T) {
	got, err := resolveVerbs([]string{"app", "db"}, []string{"incus", "docker"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []string{"incus exec app --", "docker exec db"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveVerbs = %#v, want %#v", got, want)
	}

	if v, err := resolveVerbs(nil, []string{"incus"}); err != nil || len(v) != 0 {
		t.Fatalf("zero hops must yield zero verbs, no error; got %#v err %v", v, err)
	}

	if _, err := resolveVerbs([]string{"a", "b"}, []string{"incus"}); err == nil {
		t.Fatal("more hops than the host's nest depth must be an error")
	}

	if _, err := resolveVerbs([]string{"x"}, []string{"bogus"}); err == nil {
		t.Fatal("an unknown runtime must propagate verbFor's error")
	}
}
