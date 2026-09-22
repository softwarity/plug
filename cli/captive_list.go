package main

import "strings"

// splitResolverList parses a resolver list the way Windows stores one in the
// registry: one string, servers separated by spaces or commas, sometimes both,
// sometimes trailing. Shared and pure, so the shape is proven on every OS.
func splitResolverList(raw string) []string {
	var out []string
	for _, s := range strings.FieldsFunc(raw, func(r rune) bool { return r == ' ' || r == ',' || r == ';' }) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
