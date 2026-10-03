//go:build !linux

package tun

import "os/exec"

// runChild runs cmdArgs with our stdio, forwarding INT/TERM. Off Linux there are
// no mount namespaces, so privResolv is unused (the resolver is repointed by
// configure instead) and the child runs directly.
func runChild(cmdArgs []string, _ string) (int, error) {
	return waitChild(exec.Command(cmdArgs[0], cmdArgs[1:]...))
}
