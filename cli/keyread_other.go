//go:build !windows

package main

// readKeyFile reads a profile key. Off Windows the daemon keeps the user's
// own real uid, and readUserOwnedFile proves on the descriptor it read that
// the file is theirs: there is no profile for the key to lie in.
func readKeyFile(path, _ string) ([]byte, error) { return readUserOwnedFile(path) }
