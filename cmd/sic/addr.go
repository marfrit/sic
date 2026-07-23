package main

import "fmt"

func parseTarget(s string) (host string, hops []string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			host = s[:i]
			hops = splitHops(s[i+1:])
			return
		}
	}
	host = s
	hops = nil
	return
}

func splitHops(s string) []string {
	if s == "" {
		return nil
	}
	var hops []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			hops = append(hops, s[start:i])
			start = i + 1
		}
	}
	hops = append(hops, s[start:])
	return hops
}

func verbFor(runtime, name string) (string, error) {
	switch runtime {
	case "incus":
		return "incus exec " + name + " --", nil
	case "docker":
		return "docker exec " + name, nil
	case "pct":
		return "pct exec " + name + " --", nil
	default:
		return "", fmt.Errorf("unknown runtime: %s", runtime)
	}
}
