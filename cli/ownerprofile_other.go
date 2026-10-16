//go:build !windows

package main

// profileOfFileOwner is a Windows question: everywhere else the daemon keeps
// the user's own real uid, so guardUserPath already compares the owner and no
// key needs a profile to lie in.
func profileOfFileOwner(string) string { return "" }
