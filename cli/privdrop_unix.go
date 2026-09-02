//go:build !windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// privDrop is the macOS half of the helper: it drops the child command back to the
// human user so `plug <cmd>` never runs your command as root.
//
// On macOS plug is installed as a setuid-root helper (`chown root` + `chmod u+s`,
// posted once at install). Launching it gives the process euid 0 — enough to create
// the utun and repoint the system resolver — while the REAL uid stays the user's.
// Unlike Linux file capabilities, which are dropped for free because they don't
// survive an exec, a setuid-root euid IS inherited across exec: without an explicit
// drop, the child (`npm run …`, your app) would run as root. That would wreck file
// ownership in your working tree and is exactly the kind of surprise we refuse to
// ship. So the child is spawned under the user's own credentials instead.

// applyPrivDrop makes cmd spawn as the human user when plug is running privileged.
// No-op when there is nothing to drop (see resolveDropTarget), so it's inert on the
// Linux capabilities path and on a genuine root login.
func applyPrivDrop(cmd *exec.Cmd) {
	uid, gid, ok := resolveDropTarget(os.Geteuid(), os.Getuid(), os.Getgid(),
		os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID"))
	if !ok {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cred := &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	// Carry the user's supplementary groups (staff, admin, _developer, …). Without
	// this the child would keep root's groups, which can break group-owned access.
	if groups := supplementaryGroups(uid); groups != nil {
		cred.Groups = groups
	}
	cmd.SysProcAttr.Credential = cred
}

// resolveDropTarget decides which uid/gid the child must drop to, from the process
// ids and sudo's record of the invoker. ok is false when there is nothing to drop:
//   - euid != 0        → unprivileged (Linux caps path); leave the child untouched.
//   - real uid is 0    → launched as genuine root with no SUDO_UID; don't guess a
//     user to become — run the child as root, as the user explicitly asked.
//
// The two privileged-but-droppable cases both yield ok == true:
//   - setuid-root (euid 0, real uid = the user) → drop to the real uid (macOS).
//   - `sudo plug`  (euid 0, real uid 0)         → drop to SUDO_UID/SUDO_GID.
func resolveDropTarget(euid, ruid, rgid int, sudoUID, sudoGID string) (uid, gid int, ok bool) {
	if euid != 0 {
		return 0, 0, false
	}
	uid, gid = ruid, rgid
	if uid == 0 {
		uid = atoiOr(sudoUID, 0)
		gid = atoiOr(sudoGID, 0)
	}
	if uid == 0 {
		return 0, 0, false
	}
	return uid, gid, true
}

// supplementaryGroups returns uid's group memberships as a syscall.Credential
// group list, or nil if they can't be resolved (the caller then leaves Groups nil,
// which clears root's groups rather than leaking them).
func supplementaryGroups(uid int) []uint32 {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil
	}
	gids, err := u.GroupIds()
	if err != nil {
		return nil
	}
	var out []uint32
	for _, g := range gids {
		if n, err := strconv.Atoi(g); err == nil {
			out = append(out, uint32(n))
		}
	}
	return capGroups(out, runtime.GOOS)
}

// capGroups limits the supplementary group list to what the OS's setgroups(2)
// accepts. macOS caps it at NGROUPS_MAX = 16; passing more makes the child's exec
// fail with EINVAL ("fork/exec …: invalid argument") — and a user in >16 groups is
// common (a CI runner, a corporate Mac). Keep the first 16; macOS resolves the rest
// dynamically via memberd for access checks anyway. Linux allows far more, so it is
// left untouched.
func capGroups(groups []uint32, goos string) []uint32 {
	const darwinMaxGroups = 16
	if goos == "darwin" && len(groups) > darwinMaxGroups {
		return groups[:darwinMaxGroups]
	}
	return groups
}

// chownToUser gives path back to the human user when plug runs as the macOS
// setuid helper (euid 0). Files plug writes under the user's ~/.plug — the version
// cache, a pinned known_hosts — would otherwise land root-owned, so the user could
// neither clean nor update them without sudo. No-op when unprivileged (see
// resolveDropTarget), so the Linux caps path is untouched.
func chownToUser(path string) {
	uid, gid, ok := resolveDropTarget(os.Geteuid(), os.Getuid(), os.Getgid(),
		os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID"))
	if !ok {
		return
	}
	// Lchown, not Chown: on a symlink, Chown follows it and would hand the
	// TARGET to the user — with euid 0 and a caller-controlled $HOME, that is
	// an arbitrary chown. Lchown retargets the link itself, which is harmless.
	_ = os.Lchown(path, uid, gid)
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// guardUserPath refuses a privileged write whose target resolves somewhere the
// invoking user does not own.
//
// The macOS launcher is setuid root and keeps euid 0 for its whole life (only
// the CHILD is dropped, see applyPrivDrop) — deliberately: root once at
// install, never a prompt afterwards. But every path it writes under the user's
// home is derived from $HOME, which the caller controls. Left unchecked, a
// `HOME=/tmp/x` with `/tmp/x/.plug` symlinked to a root-owned directory turns
// each of those writes into an arbitrary root write.
//
// The check is deliberately NOT "reject symlinks": people symlink their dotfiles
// (~/.plug -> ~/Dropbox/config/plug) and that must keep working. What matters is
// where the path LANDS — os.Stat resolves the whole chain, so a dotfile symlink
// into the user's own tree passes, while one into /etc does not. In other words:
// plug as root only writes where the user could have written unprivileged.
//
// Unprivileged (euid == ruid), this is a no-op: the kernel already enforces the
// user's own rights, and $HOME is theirs to point wherever they like.
func guardUserPath(path string) {
	uid, _, ok := resolveDropTarget(os.Geteuid(), os.Getuid(), os.Getgid(),
		os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID"))
	if !ok {
		return
	}
	guardPathOwnedBy(path, uid)
}

// guardFatal is how the write guards give up. A var, and only so a test can
// stand in for it: the real fatal exits the process, so the refusals below
// could not otherwise be observed from inside a test at all, and for a long
// time none of them was. Never reassigned outside tests.
var guardFatal = fatal

// guardPathOwnedBy is guardUserPath's decision, with the uid it must belong to
// passed in rather than read from the process.
//
// Split out for one reason: a test process is unprivileged, so guardUserPath
// returns at resolveDropTarget before deciding anything, and every refusal
// below it was dead as far as the suite was concerned. Turning the whole guard
// into an immediate return left the cli suite green. With the uid as an
// argument, a normal user can stand in a uid that is not theirs and watch the
// guard refuse their own files, which is the same comparison the setuid
// launcher makes for real.
func guardPathOwnedBy(path string, uid int) {
	// Walk up to the deepest component that exists: writing creates the rest,
	// and the ancestor's ownership is what decides whether we may.
	p := path
	for {
		fi, err := os.Stat(p) // follows the chain on purpose — see above
		if err == nil {
			st, okStat := fi.Sys().(*syscall.Stat_t)
			if okStat && int(st.Uid) != uid {
				guardFatal("refusing to write %s as root: it resolves to %s, owned by uid %d, not by you (uid %d).\n"+
					"      plug runs setuid so it never has to ask for a password again — it will not use that\n"+
					"      privilege to touch a file outside your own tree. Check $HOME and any symlink under it.",
					path, p, st.Uid, uid)
			}
			return
		}
		parent := filepath.Dir(p)
		if parent == p {
			return // reached the root without finding anything: nothing to judge
		}
		p = parent
	}
}

// readUserOwnedFile reads path and proves, on the DESCRIPTOR it read from, that
// the file belongs to the user plug is acting for.
//
// guardUserPath answers a question about a PATH, and the caller then opens that
// path again. Between the two, anything running as the user can swap the final
// component for a symlink to a file only root can read, and the answer the guard
// gave no longer describes what gets opened. plug is setuid on macOS, so the
// second open is root's: the file comes back, and is offered to whatever SSH
// server the caller pointed it at.
//
// O_NOFOLLOW refuses a symlink at that final component outright, and the fstat
// is taken from the descriptor already open, so the identity checked and the
// bytes returned cannot be two different files.
//
// What this does NOT close, said plainly: an ancestor DIRECTORY swapped between
// the walk and this open. Closing that needs openat2 with RESOLVE_BENEATH, which
// is Linux-only and recent, and the caller here also runs on macOS.
func readUserOwnedFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	uid, _, privileged := resolveDropTarget(os.Geteuid(), os.Getuid(), os.Getgid(),
		os.Getenv("SUDO_UID"), os.Getenv("SUDO_GID"))
	if privileged {
		fi, err := f.Stat()
		if err != nil {
			return nil, err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("cannot tell who owns %s", path)
		}
		if int(st.Uid) != uid {
			return nil, fmt.Errorf("%s belongs to uid %d, not to you (uid %d), and plug is holding a "+
				"privilege you do not have", path, st.Uid, uid)
		}
	}
	return io.ReadAll(f)
}
