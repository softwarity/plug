//go:build darwin

package tun

import (
	"os"
	"strconv"
)

// An ACCOUNT is who a cluster belongs to, written as text so the two platforms
// can disagree about what an account is without the rule having to care. Here it
// is the real uid, as a decimal: os.Getuid rather than Geteuid, because the
// launcher is setuid root and the effective uid is 0 for everyone, while the real
// one is the person whose cluster this is.
//
// Text, and this exact spelling, because the .uid sidecar it lands in is also
// read as a number by clientUIDs for the per-flow check. A decimal keeps that
// reader working; Windows writes a SID there, which that reader skips, which is
// what it already did with the -1 it used to find.
//
// Under `sudo plug` the real uid is 0 as well, and the account is then the
// person sudo records (SUDO_UID) - the same rule resolveDropTarget applies to
// the child, whose flows carry that uid: a session that registered as root
// while its command ran as the person had every flow of its own command
// refused by the daemon (owners {0}, flow 501), which the compat cell hid for
// a month behind a killed session's leftover account. Root with no SUDO_UID
// is a genuine root login, and stays 0.
func thisAccount() string { return accountOf(os.Getuid(), os.Getenv("SUDO_UID")) }

func accountOf(uid int, sudoUID string) string {
	if uid == 0 {
		if n, err := strconv.Atoi(sudoUID); err == nil && n > 0 {
			return strconv.Itoa(n)
		}
	}
	return strconv.Itoa(uid)
}

// accountHolds reports whether an account can hold a cluster against another.
//
// Root cannot: it already owns the machine, refusing it buys nothing, and the
// daemon's own probes run there. Anything that is not a positive number is not an
// account at all, and something that names nobody must not be able to hold a
// cluster against anybody.
func accountHolds(account string) bool {
	uid, err := strconv.Atoi(account)
	return err == nil && uid > 0
}
