//go:build darwin

package tun

// What an account looks like here, so the registry tests can be written once and
// mean the right thing on both platforms. A uid on macOS, a SID on Windows: the
// rule is the same, the spelling is not.
const (
	accountA      = "501"
	accountB      = "502"
	accountAlways = "0"  // root: owns the machine already, so it holds nothing
	accountNobody = "-1" // what every Windows client used to record: names no one
)
