package main

import (
	"fmt"
	"strings"
)

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

func parseHostsConfig(data string) (map[string][]string, error) {
	cfg := make(map[string][]string)
	lines := strings.Split(data, "\n")
	var currentHost string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentHost = line[1 : len(line)-1]
			if _, ok := cfg[currentHost]; !ok {
				cfg[currentHost] = nil
			}
			continue
		}
		if strings.HasPrefix(line, "nest = ") || strings.HasPrefix(line, "nest=") {
			eqIdx := strings.Index(line, "=")
			if eqIdx == -1 {
				continue
			}
			rest := strings.TrimSpace(line[eqIdx+1:])
			if !strings.HasPrefix(rest, "[") || !strings.HasSuffix(rest, "]") {
				continue
			}
			inner := rest[1 : len(rest)-1]
			if strings.TrimSpace(inner) == "" {
				cfg[currentHost] = []string{}
				continue
			}
			var items []string
			for _, item := range strings.Split(inner, ",") {
				item = strings.TrimSpace(item)
				item = strings.Trim(item, "\"")
				items = append(items, item)
			}
			cfg[currentHost] = items
		}
	}
	return cfg, nil
}
