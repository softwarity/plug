package main

import "testing"

func TestCoreMinor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"2.0.0", 0},
		{"2.1.0", 1},
		{"2.10.3", 10},
		{"3.0.0", 0},
		{"dev+abc1234", -1},
		{"", -1},
		{"2", -1},
	}
	for _, c := range cases {
		if got := coreMinor(c.in); got != c.want {
			t.Errorf("coreMinor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCoreMajor(t *testing.T) {
	cases := map[string]int{
		"1.9.3":      1, // released, predates -s → refused when -s is set
		"2.0.0":      2,
		"10.2.1":     10,
		"dev+abc123": -1, // dev build → assumed recent
		"":           -1,
		"garbage":    -1,
		".5":         -1,
	}
	for v, want := range cases {
		if got := coreMajor(v); got != want {
			t.Errorf("coreMajor(%q) = %d, want %d", v, got, want)
		}
	}
}
