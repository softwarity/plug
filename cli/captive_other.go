//go:build !darwin && !windows

package main

// captiveCollect has no collector here yet, and says so rather than guessing.
//
// The RULE is shared and tested on every OS (captive.go); what is missing is the
// per-OS half that finds the facts, because the commands differ completely:
// resolv.conf, NetworkManager or systemd-resolved on Linux, each with its own
// way of separating "what DHCP offered" from "what is configured", which is the
// comparison the whole verdict rests on. Windows has its collector now
// (captive_windows.go: the per-interface registry values).
//
// Leaving `known` false means doctor stays silent here, which is the right
// silence: a captive-portal warning that fires on facts nobody gathered would
// send people to rewrite their DNS settings for nothing.
func captiveCollect() captiveFacts { return captiveFacts{} }

// captiveRemedy is unreachable while captiveCollect reports nothing known, and
// it exists so the rule above compiles and stays honest on every OS rather than
// behind a build tag nobody runs.
func captiveRemedy(captiveFacts) string {
	return "let this network's own resolver answer while you sign in, then restore yours"
}
