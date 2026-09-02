//go:build linux

package tun

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// The shim bind-mounts a caller-named file over /etc/resolv.conf. That is safe
// only inside the namespace runChild creates for it, and nothing enforced that.
// Run from a shell it is in the MACHINE's mount namespace, and the mount still
// succeeds, because plug carries cap_sys_admin as a file capability and file
// capabilities are granted on exec whoever does the exec'ing. Any local user
// could have pointed every account's resolver at a file of their own.
//
// This process is not cloned, so it shares its parent's namespace: exactly the
// case the shim must refuse.
func TestTheShimRefusesToRunOutsideItsOwnNamespace(t *testing.T) {
	same, err := sameMountNamespaceAsParent()
	if err != nil {
		t.Skipf("cannot read the mount namespaces here (%v)", err)
	}
	if !same {
		t.Skip("this test process was cloned into its own mount namespace")
	}
	err = NsShimMain([]string{"/tmp/whatever", "/bin/true"})
	if err == nil {
		t.Fatal("the shim bind-mounted over /etc/resolv.conf from a process nobody cloned; on a machine " +
			"where plug holds cap_sys_admin that is every account's resolver, replaced by any local user")
	}
	if !strings.Contains(err.Error(), "not a command") {
		t.Errorf("it refused for the wrong reason, so it may still mount in another shape: %v", err)
	}
}

// And it must still work where it is meant to: a child cloned into a fresh mount
// namespace does NOT share its parent's, which is what lets the real path
// through. Asserted on the predicate, since entering a namespace needs privilege
// this test does not have.
func TestACloneIntoANewNamespaceIsNotTheParentsNamespace(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	cmd := exec.Command(self, "-test.run", "TestHelperReportsItsNamespace")
	cmd.Env = append(os.Environ(), "PLUG_NS_HELPER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNS}
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Unprivileged CLONE_NEWNS is refused on most hosts; that is not a
		// failure of the guard, and the case it protects is covered above.
		t.Skipf("cannot create a mount namespace here (%v)", err)
	}
	if strings.Contains(string(out), "SAME") {
		t.Error("a child cloned with CLONE_NEWNS reported sharing its parent's mount namespace, " +
			"which would make the guard refuse the one path it must allow")
	}
}

func TestHelperReportsItsNamespace(t *testing.T) {
	if os.Getenv("PLUG_NS_HELPER") != "1" {
		t.Skip("helper")
	}
	same, err := sameMountNamespaceAsParent()
	if err != nil {
		t.Skip(err)
	}
	if same {
		t.Log("SAME")
	} else {
		t.Log("DIFFERENT")
	}
}
