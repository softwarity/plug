//go:build !windows

package main

import (
	"os"
	"os/exec"
	"os/user"
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
	return out
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
	_ = os.Chown(path, uid, gid)
}

// preserveHelperPrivilege re-applies the macOS setuid-root bit to a freshly written
// launcher, so `plug self-update` doesn't silently disable the helper — a rename
// installs a new inode that has lost the bit. Only on macOS, and only when we're
// already root (the setuid helper is), so it can run without a sudo prompt and a
// capability-based Linux install is never turned into a setuid one.
func preserveHelperPrivilege(path string) {
	if runtime.GOOS != "darwin" || os.Geteuid() != 0 {
		return
	}
	_ = os.Chown(path, 0, 0)
	_ = os.Chmod(path, 0o755|os.ModeSetuid)
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
