//go:build linux

package tun

import (
	"os"
	"strings"
	"testing"
)

// The shim bind-mounts a caller-named file over /etc/resolv.conf, which is safe
// only inside a mount namespace of its own. It used to rely on its caller having
// made one: run straight from a shell it was in the MACHINE's namespace, and the
// mount still succeeded, because plug carries CAP_SYS_ADMIN as a file capability
// and file capabilities are granted on exec whoever does the exec'ing. Any local
// account could point every other account's resolver at a file of its own.
//
// It makes its own namespace now, before touching anything. What this asserts is
// the ordering that makes that true: without the privilege to create one, it must
// REFUSE rather than fall through to the mount. A test binary has no
// CAP_SYS_ADMIN, so that is the path taken here.
func TestTheShimWillNotMountWithoutANamespaceOfItsOwn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the unshare succeeds and the mount is contained, which is the other path")
	}
	err := NsShimMain([]string{"/tmp/whatever", "/bin/true"})
	if err == nil {
		t.Fatal("the shim bind-mounted without a namespace of its own; on a machine where plug holds " +
			"cap_sys_admin that is every account's resolver, replaced by any local user")
	}
	if !strings.Contains(err.Error(), "private mount namespace") {
		t.Errorf("it stopped for some other reason, so the ordering this protects is not what stopped "+
			"it: %v", err)
	}
}

// And it must still refuse the shapes it always refused, so the new step did not
// swallow the argument checking that used to come first.
func TestTheShimStillChecksItsArguments(t *testing.T) {
	if err := NsShimMain(nil); err == nil {
		t.Error("the shim accepted no arguments at all")
	}
	if err := NsShimMain([]string{"only-one"}); err == nil {
		t.Error("the shim accepted a resolv path with no command to run")
	}
}
