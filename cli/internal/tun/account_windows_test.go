//go:build windows

package tun

// See account_darwin_test.go. These are well-formed SIDs of the shape a domain
// or local account gets; only their form matters to the rule.
const (
	accountA      = "S-1-5-21-1111111111-2222222222-3333333333-1001"
	accountB      = "S-1-5-21-1111111111-2222222222-3333333333-1002"
	accountAlways = "S-1-5-18" // LocalSystem: runs the service, so it holds nothing
	accountNobody = "-1"       // what every Windows client used to record
)
