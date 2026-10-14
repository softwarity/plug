//go:build darwin

package tun

import (
	"reflect"
	"testing"
)

// pgrep's output, one pid per line, is what the HUP is sent to. Never pid 1
// (launchd) or 0, whatever a garbled line says.
func TestTheHUPGoesToThePIDsPgrepListed(t *testing.T) {
	got := parsePIDs("412\n 9187\nnot-a-pid\n1\n0\n")
	if want := []int{412, 9187}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePIDs = %v, want %v", got, want)
	}
	if got := parsePIDs(""); len(got) != 0 {
		t.Fatalf("no output must signal nobody, got %v", got)
	}
}
