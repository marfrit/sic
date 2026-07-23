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

func resolveVerbs(hops []string, nest []string) ([]string, error) {
	if len(hops) > len(nest) {
		return nil, fmt.Errorf("too many hops for this host's nest depth")
	}
	if len(hops) == 0 {
		return []string{}, nil
	}
	verbs := make([]string, len(hops))
	for i, hop := range hops {
		verb, err := verbFor(nest[i], hop)
		if err != nil {
			return nil, err
		}
		verbs[i] = verb
	}
	return verbs, nil
}
