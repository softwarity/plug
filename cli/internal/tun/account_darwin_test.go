//go:build darwin

package tun

import "testing"

// What an account looks like here, so the registry tests can be written once and
// mean the right thing on both platforms. A uid on macOS, a SID on Windows: the
// rule is the same, the spelling is not.
const (
	accountA      = "501"
	accountB      = "502"
	accountAlways = "0"  // root: owns the machine already, so it holds nothing
	accountNobody = "-1" // what every Windows client used to record: names no one
)

// The account a session registers is the PERSON's uid: the real uid when plug
// is the setuid helper, SUDO_UID under sudo (where the real uid is 0 too), and
// 0 only for a genuine root login. The child's flows carry that uid, and the
// per-flow check compares against it.
func TestAccountOfUnderSudo(t *testing.T) {
	if accountOf(501, "") != "501" {
		t.Fatal("setuid: the real uid")
	}
	if accountOf(0, "501") != "501" {
		t.Fatal("sudo: the person sudo records")
	}
	if accountOf(0, "") != "0" || accountOf(0, "x") != "0" || accountOf(0, "0") != "0" {
		t.Fatal("root with no usable SUDO_UID stays root")
	}
	if accountOf(501, "502") != "501" {
		t.Fatal("not root: SUDO_UID is not consulted")
	}
}
