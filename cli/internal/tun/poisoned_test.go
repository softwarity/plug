//go:build darwin

package tun

import "testing"

// The teardown restores exactly what was captured at startup. When a previous
// session died without its teardown, what is there to capture is plug's own
// resolver - and restoring THAT on a clean exit hands the breakage to the next
// session, which captures it in turn. Every session of a day then "breaks DNS"
// while every one of them is faithfully putting back the first one's wreckage.
// A captured dict that points into the fake range is not the machine's state.
func TestAPlugLeftoverIsNeverKeptAsTheStateToRestore(t *testing.T) {
	for _, servers := range [][]string{
		{"198.18.0.53"},                  // this instance's own resolver
		{"198.19.4.53"},                  // another cluster's, same range
		{"192.168.1.254", "198.18.0.53"}, // mixed: still not the machine's
		{" 198.18.0.53 "},                // as scutil prints it
	} {
		if !poisonedByPlug(servers) {
			t.Errorf("%v was accepted as the machine's own DNS", servers)
		}
	}
}

// And the machine's real resolvers must never be mistaken for a leftover, or
// the teardown would drop a key it should have restored - the opposite bug.
func TestTheMachinesOwnResolversAreKept(t *testing.T) {
	for _, servers := range [][]string{
		{"192.168.1.254", "2001:861:8ac4:d650:ba8c:2bff:fe14:ac84"},
		{"192.0.2.254", "192.0.2.253"}, // a VPN's
		{"1.1.1.1"},
		{},
		nil,
	} {
		if poisonedByPlug(servers) {
			t.Errorf("%v was taken for a plug leftover", servers)
		}
	}
}
