package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/softwarity/plug/cli/internal/tun"
	"golang.org/x/sys/windows"
)

// Windows has the SMB client of them all, and it dictates the shape here: the
// redirector speaks to port 445 only - a UNC path carries no port - and the
// system's own server holds that port on every local address. So no local
// forward: the share is reached BY NAME, `\\<helper>\vol`, and the name goes
// where every cluster name goes on this machine, through plug's own DNS to a
// fake address routed into the datapath, then down the tunnel to the helper.
// The router attributes that flow to the session's cluster by the name the
// session pinned (tun.PinName): the kernel's SMB client is nobody's child, and
// no ancestry would lead to a launcher.
//
// Where it lands: a DRIVE LETTER. A directory cannot point at a share without
// a privilege (a directory symlink needs SeCreateSymbolicLinkPrivilege, or
// Developer Mode); a drive letter needs nothing, and a variable repointed at
// `Z:\` is as good as one repointed at a directory. An automatic mount takes a
// free letter from Z downwards; an explicit --mount names one (`--mount
// /data:Z:`), or a directory, for which the symlink is tried and, refused,
// the letter is suggested.

func mountSupported() error { return nil }

func mountBindAddr() string { return "" }

func mountUsesForward() bool { return false }

// autoMountPath is "" on Windows: the letter is picked when the mount is
// made, not before.
func autoMountPath(dir, clusterPath string) string { return "" }

func mountSMBShare(t mountTarget, path string) (string, error) {
	unc := `\\` + t.host + `\` + t.share
	switch {
	case path == "":
		letter, err := freeDriveLetter()
		if err != nil {
			return "", err
		}
		if err := netUse(letter, unc, t.user, t.pass); err != nil {
			return "", err
		}
		return letter + `\`, nil
	case isDriveLetter(path):
		letter := strings.ToUpper(path[:2])
		if err := netUse(letter, unc, t.user, t.pass); err != nil {
			return "", err
		}
		return letter + `\`, nil
	default:
		// A directory: authenticate to the share, then a directory symlink at
		// the path. Refused without the privilege, and the message says what
		// to do instead.
		if err := netUse("", unc, t.user, t.pass); err != nil {
			return "", err
		}
		if st, err := os.Lstat(path); err == nil {
			if st.Mode()&os.ModeSymlink == 0 {
				_ = netUseDelete(unc)
				return "", fmt.Errorf("%s exists and is not a link", path)
			}
			_ = os.Remove(path) // a previous session's link
		}
		if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
			_ = netUseDelete(unc)
			return "", err
		}
		if out, err := exec.Command("cmd", "/c", "mklink", "/D", path, unc).CombinedOutput(); err != nil {
			_ = netUseDelete(unc)
			return "", fmt.Errorf("cannot link %s to %s (%s): a directory needs the symlink privilege (Developer Mode, or an elevated shell); use a drive letter instead: --mount %s:Z:", path, unc, strings.TrimSpace(string(out)), t.share)
		}
		return path, nil
	}
}

func unmountSMBShare(path string) error {
	if !mountedAt(path) {
		return nil
	}
	if isDriveLetter(path) {
		return netUseDelete(strings.ToUpper(path[:2]))
	}
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return netUseDelete(target)
}

// mountedAt: a drive letter is mounted when it is a REMOTE drive; a directory
// when it is a symlink to a UNC path.
func mountedAt(path string) bool {
	if isDriveLetter(path) {
		root, err := windows.UTF16PtrFromString(strings.ToUpper(path[:2]) + `\`)
		if err != nil {
			return false
		}
		return windows.GetDriveType(root) == windows.DRIVE_REMOTE
	}
	target, err := os.Readlink(path)
	return err == nil && strings.HasPrefix(target, `\\`)
}

// isDriveLetter: "Z:", "Z:\" or "Z:/".
func isDriveLetter(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
		return false
	}
	return len(p) == 2 || (len(p) == 3 && (p[2] == '\\' || p[2] == '/'))
}

// freeDriveLetter picks the highest letter no drive uses, Z downwards, so it
// never collides with what the machine already has (which climbs from C).
func freeDriveLetter() (string, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return "", err
	}
	for c := 'Z'; c >= 'D'; c-- {
		if mask&(1<<uint(c-'A')) == 0 {
			return string(c) + ":", nil
		}
	}
	return "", errors.New("no free drive letter for the mount")
}

// netUse maps the share to a drive letter (or, with "", only authenticates to
// it). Not persistent: the mount is the session's. The password travels on
// the command line, as it does with mount_smbfs: a session secret, gone with
// the helper.
func netUse(letter, unc, user, pass string) error {
	args := []string{"use"}
	if letter != "" {
		args = append(args, letter)
	}
	args = append(args, unc, "/user:"+user, pass, "/persistent:no")
	if out, err := exec.Command("net", args...).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("net use %s %s: %s", letter, unc, msg)
	}
	return nil
}

func netUseDelete(what string) error {
	if out, err := exec.Command("net", "use", what, "/delete", "/y").CombinedOutput(); err != nil {
		return fmt.Errorf("net use %s /delete: %s", what, strings.TrimSpace(string(out)))
	}
	return nil
}

func filepathDir(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	if i <= 0 {
		return p
	}
	return p[:i]
}

// pinMountName tells the machine's datapath that the helper's name is this
// session's cluster's: the kernel's SMB client reaches it through that
// datapath, and no ancestry of the kernel leads to a launcher.
func pinMountName(cfg config, host string) {
	tun.PinName(cfg.host+":"+cfg.port, os.Getpid(), host)
}

func unpinMountNames(cfg config) {
	tun.UnpinNames(cfg.host+":"+cfg.port, os.Getpid())
}
