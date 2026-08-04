package main

import (
	"strconv"
	"testing"
	"time"
)

// The grace has a derivation, not a feeling: Docker's embedded DNS hands every
// caller a 600s TTL on cluster names, so a linger shorter than that protects
// nothing — the caller is still allowed to dial the old address when the
// signpost dies. Guard the relationship, not the number.
func TestLingerGraceOutlivesDockersDNSTTL(t *testing.T) {
	const dockerEmbeddedDNSTTL = 600 * time.Second
	if lingerGrace <= dockerEmbeddedDNSTTL {
		t.Fatalf("lingerGrace (%v) must outlive Docker's 600s DNS TTL, or callers outlive the address", lingerGrace)
	}
}

func TestLingerExpiry(t *testing.T) {
	now := time.Now()
	stamp := func(ago time.Duration) string { return strconv.FormatInt(now.Add(-ago).Unix(), 10) }
	cases := []struct {
		what    string
		stamp   string
		expired bool
	}{
		{"not lingering at all (live session, or crash leftover — other rules own those)", "", false},
		{"just unserved", stamp(time.Second), false},
		{"deep in the grace", stamp(lingerGrace - time.Minute), false},
		{"past the grace", stamp(lingerGrace + time.Minute), true},
		{"mangled stamp — reaping is the honest direction", "yesterday", true},
	}
	for _, c := range cases {
		if got := lingerExpired(c.stamp, now); got != c.expired {
			t.Errorf("%s: lingerExpired(%q) = %v, want %v", c.what, c.stamp, got, c.expired)
		}
	}
}
