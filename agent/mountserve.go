package agent

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/md4"
)

// mount-serve is the live-mount HELPER's process: the agent starts this same
// image beside a workload with one of its volumes mounted at mountVolumePath,
// and this turns it into an SMB server for that one directory, for one
// session, behind one throwaway credential. The developer's OS mounts it
// through the tunnel with the client it already has.
//
// Everything arrives through the environment, set by whoever created the
// container (agent/mount.go): the credential, and the share name the client
// will ask for. The volume's path is a constant of the contract.
//
// Files land under the volume owner's uid/gid, not root's: the helper reads
// who owns the volume's root and makes Samba write as that identity (`force
// user`), so what the developer saves is what the workload can read back when
// it comes out of the park. A volume owned by root is served as root.
const (
	mountVolumePath = "/mnt/vol"
	smbUserEnv      = "PLUG_SMB_USER"
	smbPassEnv      = "PLUG_SMB_PASS"
	smbShareEnv     = "PLUG_SMB_SHARE"
	smbDefaultShare = "vol"
	smbConfPath     = "/etc/samba/smb.conf"
	smbPasswdPath   = "/var/lib/samba/private/smbpasswd" // SMB_PASSWD_FILE, as `smbd -b` reports it
	smbPort         = 445
)

// smbConf renders the whole Samba configuration for one share. SMB2 at least
// (the protocol every client here speaks; SMB1 is what nobody should), no
// NetBIOS and no printers - none of the discovery machinery, since the client
// reaches this container by the name plug hands it and never by browsing.
// `fruit` is the Apple compatibility module: without it macOS lays `._` files
// next to everything it touches to hold what it cannot store as xattrs.
func smbConf(share, user, forceUser string, fruit bool) string {
	var b strings.Builder
	b.WriteString("[global]\n")
	b.WriteString("  security = user\n")
	b.WriteString("  map to guest = Never\n")
	b.WriteString("  passdb backend = smbpasswd\n")
	b.WriteString("  server min protocol = SMB2_02\n")
	b.WriteString("  smb ports = " + strconv.Itoa(smbPort) + "\n")
	b.WriteString("  disable netbios = yes\n")
	b.WriteString("  load printers = no\n")
	b.WriteString("  printing = bsd\n")
	b.WriteString("  printcap name = /dev/null\n")
	b.WriteString("  disable spoolss = yes\n")
	b.WriteString("  log level = 1\n")
	b.WriteString("  ea support = yes\n")
	if fruit {
		b.WriteString("  vfs objects = fruit streams_xattr\n")
		b.WriteString("  fruit:metadata = stream\n")
		b.WriteString("  fruit:veto_appledouble = no\n")
	}
	b.WriteString("\n[" + share + "]\n")
	b.WriteString("  path = " + mountVolumePath + "\n")
	b.WriteString("  read only = no\n")
	b.WriteString("  browseable = no\n")
	b.WriteString("  valid users = " + user + "\n")
	b.WriteString("  force user = " + forceUser + "\n")
	b.WriteString("  create mask = 0664\n")
	b.WriteString("  directory mask = 0775\n")
	return b.String()
}

// mountServe is the argv mode: the container's whole process. smbd runs as its
// child and the container's stop signal is relayed to it, so `docker stop` or
// a pod deletion ends the server cleanly; when smbd dies, so does this, and
// the container's restart policy decides what happens next.
func mountServe() {
	user := os.Getenv(smbUserEnv)
	pass := os.Getenv(smbPassEnv)
	if user == "" || pass == "" {
		fatal("plug-agent mount-serve: %s and %s must be set", smbUserEnv, smbPassEnv)
	}
	share := os.Getenv(smbShareEnv)
	if share == "" {
		share = smbDefaultShare
	}
	if _, err := os.Stat(mountVolumePath); err != nil {
		fatal("plug-agent mount-serve: %s is not mounted: %v", mountVolumePath, err)
	}
	uid, gid := ownerIDs(mountVolumePath)
	// The SMB account is a Unix account to Samba, so it has to exist. It is
	// given the volume owner's ids so that the account and `force user` agree;
	// a root-owned volume is served as root, which needs no account of its own.
	forceUser := "root"
	if uid != 0 {
		forceUser = user
	}
	if user != "root" {
		if err := ensureUnixUser(user, uid, gid); err != nil {
			fatal("plug-agent mount-serve: creating the %s account: %v", user, err)
		}
	}
	_, ferr := os.Stat("/usr/lib/samba/vfs/fruit.so")
	if err := os.MkdirAll(filepath.Dir(smbConfPath), 0o755); err != nil {
		fatal("plug-agent mount-serve: %v", err)
	}
	if err := os.WriteFile(smbConfPath, []byte(smbConf(share, user, forceUser, ferr == nil)), 0o644); err != nil {
		fatal("plug-agent mount-serve: writing %s: %v", smbConfPath, err)
	}
	// The account's NT hash, written straight into Samba's smbpasswd file:
	// no smbpasswd binary (it lives in a package this image does not carry),
	// nothing on a command line, and the password itself never touches disk.
	if err := os.MkdirAll(filepath.Dir(smbPasswdPath), 0o700); err != nil {
		fatal("plug-agent mount-serve: %v", err)
	}
	if err := os.WriteFile(smbPasswdPath, []byte(smbPasswdLine(user, uid, pass, time.Now())), 0o600); err != nil {
		fatal("plug-agent mount-serve: writing %s: %v", smbPasswdPath, err)
	}
	fmt.Fprintf(os.Stderr, "plug-agent mount-serve: serving %s as //%s/%s on :%d (files written as %s)\n",
		mountVolumePath, user, share, smbPort, forceUser)
	// -F foreground, no daemon fork, logs on stdout (the container's log).
	smbd := exec.Command("/usr/sbin/smbd", "-F", "--debug-stdout", "--no-process-group", "-s", smbConfPath)
	smbd.Stdout, smbd.Stderr = os.Stdout, os.Stderr
	if err := smbd.Start(); err != nil {
		fatal("plug-agent mount-serve: starting smbd: %v", err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sig
		_ = smbd.Process.Signal(s)
	}()
	if err := smbd.Wait(); err != nil {
		fatal("plug-agent mount-serve: smbd: %v", err)
	}
}

// ownerIDs is the uid/gid owning path, asked of the coreutils rather than of
// the kernel: this file builds everywhere the agent does (nothing here is
// behind a build tag) and mount-serve only ever runs in the Alpine image,
// whose busybox stat knows -c. Unreadable is root: the safe default, since
// root can always write, and the log says which identity was used.
func ownerIDs(path string) (uid, gid int) {
	out, err := exec.Command("stat", "-c", "%u %g", path).Output()
	if err != nil {
		return 0, 0
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return 0, 0
	}
	uid, _ = strconv.Atoi(f[0])
	gid, _ = strconv.Atoi(f[1])
	return uid, gid
}

// ensureUnixUser creates a system account carrying the given ids when none
// by that name exists, with busybox's adduser/addgroup - what Alpine has.
func ensureUnixUser(name string, uid, gid int) error {
	if passwdHas(mustRead("/etc/passwd"), name) {
		return nil
	}
	group := "root"
	if gid != 0 {
		// The volume's gid may already have a group (it usually does not);
		// reuse it rather than fail on a duplicate.
		if g := groupByGID(mustRead("/etc/group"), gid); g != "" {
			group = g
		} else {
			group = name
			if out, err := exec.Command("addgroup", "-g", strconv.Itoa(gid), group).CombinedOutput(); err != nil {
				return fmt.Errorf("addgroup: %v: %s", err, strings.TrimSpace(string(out)))
			}
		}
	}
	args := []string{"-D", "-H", "-G", group}
	if uid != 0 {
		args = append(args, "-u", strconv.Itoa(uid))
	}
	args = append(args, name)
	if out, err := exec.Command("adduser", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("adduser: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func mustRead(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

// passwdHas reports whether a passwd file names the account.
func passwdHas(passwd []byte, name string) bool {
	for _, line := range strings.Split(string(passwd), "\n") {
		if strings.HasPrefix(line, name+":") {
			return true
		}
	}
	return false
}

// groupByGID returns the name of the group carrying gid in a group file, or "".
func groupByGID(group []byte, gid int) string {
	want := strconv.Itoa(gid)
	for _, line := range strings.Split(string(group), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 3 && f[2] == want {
			return f[0]
		}
	}
	return ""
}

// ntHash is the NT password hash Samba stores: MD4 over the password in
// UTF-16LE, upper-case hex. MD4 is not a choice, it is the protocol - what
// NTLM has always been, and the only form an smbpasswd file takes.
func ntHash(password string) string {
	u := utf16.Encode([]rune(password))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	h := md4.New()
	h.Write(b)
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

// smbPasswdLine is one account in smbpasswd(5) format:
//
//	name:uid:LM hash:NT hash:[flags]:LCT-<hex last change>:
//
// 32 X's for the LM hash is "no LM password" (LANMAN is what nobody should
// accept); U is a plain user account, the flag field 11 wide, padded.
func smbPasswdLine(user string, uid int, password string, now time.Time) string {
	return fmt.Sprintf("%s:%d:%s:%s:[U          ]:LCT-%08X:\n",
		user, uid, strings.Repeat("X", 32), ntHash(password), now.Unix())
}
