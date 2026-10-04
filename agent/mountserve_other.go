//go:build !unix

package agent

import "errors"

// mount-serve only ever runs in the Linux image; these keep the package
// building where the agent's other modes are compiled.

func ownerIDs(string) (uid, gid int, err error) {
	return 0, 0, errors.New("file ownership is not read on this platform")
}

func becomeIDs(int, int) error { return errors.New("identity is not changed on this platform") }
