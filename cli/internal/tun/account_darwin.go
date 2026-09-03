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
func thisAccount() string { return strconv.Itoa(os.Getuid()) }

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
