//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The write guards are the only thing standing between a setuid-root plug and
// a $HOME the caller chose. They end in fatal(), which exits the process, so
// for a long time nothing asserted that they refuse anything at all: replacing
// guardUserPath's body with a bare return left this entire suite green. These
// tests take the refusals themselves, through the same walk the launcher runs.

// refused is what the stand-in fatal panics with, so a refusal can be unwound
// and read instead of ending the test binary.
type refused string

// guardRefusal runs fn with the guards' fatal standing in a panic and returns
// the refusal, or "" when the guard let the write through. Any other panic is
// re-raised: a nil map or a bad index must not read as "the guard refused".
func guardRefusal(t *testing.T, fn func()) (msg string) {
	t.Helper()
	saved := guardFatal
	t.Cleanup(func() { guardFatal = saved })
	guardFatal = func(format string, a ...any) { panic(refused(fmt.Sprintf(format, a...))) }
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if got, ok := r.(refused); ok {
			msg = string(got)
			return
		}
		panic(r)
	}()
	fn()
	return ""
}

// stranger is a uid that is not the one running the tests. Nothing is created
// under it: it is handed to the guard as "the user", which makes the test's own
// files foreign to the guard. That is the only way a normal user can watch the
// ownership comparison fail, since they cannot create a file owned by anyone
// else, and it is exactly the comparison the setuid launcher makes for real.
func stranger() int { return os.Getuid() + 1 }

// The failure this prevents: plug running as root, $HOME pointed at a tree the
// invoker does not own (or a symlink under it that lands there), writing a key
// or a config where the user could never have written it themselves.
func TestGuardUserPathRefusesAPathOutsideTheUsersTree(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", ".plug")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "known_hosts")
	if err := os.WriteFile(target, []byte("host key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	msg := guardRefusal(t, func() { guardPathOwnedBy(target, stranger()) })
	if msg == "" {
		t.Fatalf("the guard wrote %s as root although it is owned by uid %d, not by the invoker (uid %d)",
			target, os.Getuid(), stranger())
	}
	// The refusal has to name the path that decided it and both uids, or the
	// user cannot tell which link under $HOME sent the write out of their tree.
	for _, want := range []string{target, "owned by uid " + strconv.Itoa(os.Getuid()), "uid " + strconv.Itoa(stranger())} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q, so it cannot be acted on:\n%s", want, msg)
		}
	}
}

// The guard must judge the deepest component that EXISTS, not the file being
// written: every one of its callers is about to create that file. Judging only
// the leaf would make the check pass on everything plug writes for the first
// time, which is most of what it writes.
func TestGuardUserPathJudgesTheDeepestExistingAncestor(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "home")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	// Nothing below "home" exists yet, which is the normal case on a first run.
	target := filepath.Join(existing, ".plug", "profiles", "default", "id_ed25519")

	msg := guardRefusal(t, func() { guardPathOwnedBy(target, stranger()) })
	if msg == "" {
		t.Fatalf("a file about to be created under %s, which the invoker does not own, was allowed", existing)
	}
	if !strings.Contains(msg, existing) {
		t.Errorf("the refusal names no existing ancestor, so the guard judged a component that is not there:\n%s", msg)
	}
}

// The other half: a path entirely inside the user's own tree must go through
// untouched. A guard that refuses too much is uninstallable, and this one runs
// in front of every write plug makes under $HOME.
func TestGuardUserPathLetsTheUsersOwnTreeThrough(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home", ".plug")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	// Owned by the invoker, and deliberately wide open: guardUserPath judges
	// ownership only. A directory the user owns is one they could have written
	// unprivileged whatever its mode, so its mode is not root's business here.
	// The mode rule belongs to the store guard, where the owner is root.
	if err := os.Chmod(home, 0o777); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "profiles", "default", "id_ed25519")

	if msg := guardRefusal(t, func() { guardPathOwnedBy(target, os.Getuid()) }); msg != "" {
		t.Fatalf("the guard refused a path the user owns outright:\n%s", msg)
	}
}

// The case the guard must explicitly not break: people symlink their dotfiles
// (~/.plug -> ~/Dropbox/config/plug). "Refuse symlinks" would be the easy
// hardening and it would lock those users out, so what is checked is where the
// path LANDS: inside the user's tree it passes, and the link is no free pass
// either, since the same link judged for another user is still refused.
func TestGuardUserPathFollowsADotfileSymlinkInsideTheUsersTree(t *testing.T) {
	dir := t.TempDir()
	dropbox := filepath.Join(dir, "Dropbox", "config", "plug")
	if err := os.MkdirAll(dropbox, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".plug")
	if err := os.Symlink(dropbox, link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(link, "known_hosts")

	if msg := guardRefusal(t, func() { guardPathOwnedBy(target, os.Getuid()) }); msg != "" {
		t.Fatalf("a dotfile symlink into the user's own tree was refused, which breaks a supported setup:\n%s", msg)
	}
	if msg := guardRefusal(t, func() { guardPathOwnedBy(target, stranger()) }); msg == "" {
		t.Fatal("a symlinked .plug was waved through: a link must be resolved and judged, not skipped")
	}

	// And the chain is really resolved, not merely tolerated: point .plug at
	// something that is not there and the link stops being a component to
	// judge. What decides is then the deepest directory that actually exists,
	// which is the one the refusal must name. Judging the link itself (Lstat)
	// would report a path whose ownership says nothing about where the write
	// would land.
	broken := filepath.Join(dir, "broken")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(broken, ".plug")
	if err := os.Symlink(filepath.Join(dir, "gone"), dangling); err != nil {
		t.Fatal(err)
	}
	msg := guardRefusal(t, func() { guardPathOwnedBy(filepath.Join(dangling, "known_hosts"), stranger()) })
	if !strings.Contains(msg, "resolves to "+broken+",") {
		t.Errorf("the guard judged the link instead of what it resolves to, and named the wrong path:\n%s", msg)
	}
}
