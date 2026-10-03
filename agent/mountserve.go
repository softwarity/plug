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
// container (agent/mount.go): the credential (in the environment, or in a file
// the orchestrator mounts: a Swarm secret), the share name the client will ask
// for, and the addresses the agent connects from. The volume's path is a
// constant of the contract.
//
// Files land under the volume owner's uid/gid, not root's: the helper reads
// who owns the volume's root and makes Samba write as that identity (`force
// user`), so what the developer saves is what the workload can read back when
// it comes out of the park. A volume owned by root is served as root.
//
// That is the ROOT path, the one Docker, Swarm and a classic Kubernetes run.
// OpenShift and OKD run every pod under their `restricted` SCC: an arbitrary
// uid from the namespace's range, group 0, every capability dropped, and a pod
// that names its own uid is refused. Under it the helper cannot bind 445,
// cannot write /etc/samba or /var/lib/samba, cannot create a Unix account and
// cannot `force user`. So mount-serve takes one decision at start, whether it
// is root, and the unprivileged shape follows from it: smbd listens on the
// port PLUG_SMB_LISTEN names (the Service in front of the pod still answers
// 445, which is the only port the client is ever told), keeps every file under
// a directory of its own, and resolves the SMB account through nss_wrapper
// (nssWrapperFiles) since an arbitrary uid is in no /etc/passwd. Files are
// then written by the pod's uid, which is what OpenShift arranges volumes for
// (group 0 writable). Not an option: a decision the process takes about
// itself, so a root run stays exactly what it was.
const (
	mountVolumePath = "/mnt/vol"
	smbUserEnv      = "PLUG_SMB_USER"
	smbPassEnv      = "PLUG_SMB_PASS"
	smbPassFileEnv  = "PLUG_SMB_PASS_FILE" // the password's file, when it is not in the environment (Swarm secret)
	smbShareEnv     = "PLUG_SMB_SHARE"
	smbAllowEnv     = "PLUG_SMB_ALLOW"  // space-separated addresses that may connect; empty means no filter
	smbNoteEnv      = "PLUG_SMB_NOTE"   // one line the agent wants in the helper's log (a fallback it took)
	smbListenEnv    = "PLUG_SMB_LISTEN" // the port smbd binds; smbPort unless set (a pod that cannot bind below 1024)
	smbDefaultShare = "vol"
	smbConfPath     = "/etc/samba/smb.conf"
	smbPasswdPath   = "/var/lib/samba/private/smbpasswd" // SMB_PASSWD_FILE, as `smbd -b` reports it
	smbPort         = 445                                // the port the CLIENT is told (mountHelperPort), and the default listen port
)

// nssWrapperLib is where Alpine's nss_wrapper package puts the library smbd is
// preloaded with on the unprivileged path. A variable so a test can point it
// at nothing and read the refusal.
var nssWrapperLib = "/usr/lib/libnss_wrapper.so"

// smbOptions is everything smbConf renders: the share and its account, the
// addresses allowed in, the port to bind, and the two things that differ
// between the root and the unprivileged shape. forceUser "" writes no `force
// user` line (files are then the smbd process's own); stateDir "" leaves
// Samba's compiled-in layout alone (/var/lib/samba, /run/samba, /var/log/samba:
// root's), anything else puts every runtime directory under it.
type smbOptions struct {
	share, user, forceUser string
	fruit                  bool
	allow                  []string
	listen                 int
	stateDir               string
}

// smbStateDirs are the directories smbd opens for itself: each Samba option
// and the subdirectory it is given under stateDir. Created before smbd starts,
// since smbd makes some of them on its own and not others.
var smbStateDirs = [][2]string{
	{"lock directory", "lock"}, {"state directory", "state"}, {"cache directory", "cache"},
	{"private dir", "private"}, {"pid directory", "pid"}, {"ncalrpc dir", "ncalrpc"},
}

// passwdFile is where the account file goes: Samba's own private dir on the
// root path, the state directory otherwise.
func (o smbOptions) passwdFile() string {
	if o.stateDir == "" {
		return smbPasswdPath
	}
	return filepath.Join(o.stateDir, "smbpasswd")
}

// confFile is where the configuration goes, by the same rule.
func (o smbOptions) confFile() string {
	if o.stateDir == "" {
		return smbConfPath
	}
	return filepath.Join(o.stateDir, "smb.conf")
}

// smbConf renders the whole Samba configuration for one share. SMB2 at least
// (the protocol every client here speaks; SMB1 is what nobody should), no
// NetBIOS and no printers - none of the discovery machinery, since the client
// reaches this container by the name plug hands it and never by browsing.
// `fruit` is the Apple compatibility module: without it macOS lays `._` files
// next to everything it touches to hold what it cannot store as xattrs.
//
// allow is the list of addresses a connection may come from: the AGENT's, and
// nothing else, because the developer's SMB client never reaches this port
// itself. It rides a direct-tcpip channel to the agent, and the agent dials
// the helper, so every legitimate connection carries the agent's address as
// its source. Anything else on the application network (a workload, a
// neighbour's container) is refused by Samba before it can try the password.
// An empty list sets NO filter: an agent unsure of its addresses must not turn
// a working mount into a refusal, it says so in the log instead.
//
// Signing is mandatory and encryption desired, never required: every SMB2/3
// client here (mount_smbfs, the cifs module, the Windows redirector) signs
// when the server asks and needs no option for it; encryption is taken when
// the client offers it (SMB3) and a client that does not still mounts.
//
// The listen port is the helper's own business: the client is always told
// smbPort, and whatever stands between (the Service on Kubernetes, nothing on
// Docker) is what maps one to the other.
func smbConf(o smbOptions) string {
	var b strings.Builder
	b.WriteString("[global]\n")
	b.WriteString("  security = user\n")
	b.WriteString("  map to guest = Never\n")
	b.WriteString("  passdb backend = smbpasswd\n")
	if o.stateDir != "" {
		// Every path smbd opens for itself, under one directory it owns: the
		// compiled-in ones are root's. The account file goes with them.
		b.WriteString("  smb passwd file = " + o.passwdFile() + "\n")
		for _, d := range smbStateDirs {
			b.WriteString("  " + d[0] + " = " + filepath.Join(o.stateDir, d[1]) + "\n")
		}
		b.WriteString("  log file = " + filepath.Join(o.stateDir, "log.smbd") + "\n")
	}
	b.WriteString("  server min protocol = SMB2_02\n")
	b.WriteString("  server signing = mandatory\n")
	b.WriteString("  smb encrypt = desired\n")
	if len(o.allow) > 0 {
		b.WriteString("  hosts allow = " + strings.Join(o.allow, " ") + "\n")
	}
	b.WriteString("  smb ports = " + strconv.Itoa(o.listen) + "\n")
	b.WriteString("  disable netbios = yes\n")
	b.WriteString("  load printers = no\n")
	b.WriteString("  printing = bsd\n")
	b.WriteString("  printcap name = /dev/null\n")
	b.WriteString("  disable spoolss = yes\n")
	b.WriteString("  log level = 1\n")
	b.WriteString("  ea support = yes\n")
	if o.fruit {
		b.WriteString("  vfs objects = fruit streams_xattr\n")
		b.WriteString("  fruit:metadata = stream\n")
		b.WriteString("  fruit:veto_appledouble = no\n")
	}
	b.WriteString("\n[" + o.share + "]\n")
	b.WriteString("  path = " + mountVolumePath + "\n")
	b.WriteString("  read only = no\n")
	b.WriteString("  browseable = no\n")
	b.WriteString("  valid users = " + o.user + "\n")
	if o.forceUser != "" {
		b.WriteString("  force user = " + o.forceUser + "\n")
	}
	b.WriteString("  create mask = 0664\n")
	b.WriteString("  directory mask = 0775\n")
	return b.String()
}

// smbListenPort reads PLUG_SMB_LISTEN: unset is smbPort, anything else must be
// a port.
func smbListenPort(env string) (int, error) {
	if env == "" {
		return smbPort, nil
	}
	p, err := strconv.Atoi(env)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%s=%q is not a port", smbListenEnv, env)
	}
	return p, nil
}

// nssWrapperFiles is the passwd and group pair nss_wrapper serves smbd on the
// unprivileged path. Samba turns the SMB account into a Unix one through NSS,
// and an arbitrary uid is in no /etc/passwd; and it wants the guest account
// (`nobody`) to exist at start even though `map to guest = Never` means no
// connection ever becomes it. Two users, two groups, nothing else: a lookup
// of any other uid (a file on the volume owned by someone else) fails and
// Samba maps it to a Unix-users SID, which is fine for a share nobody browses
// by owner.
func nssWrapperFiles(user string, uid, gid int) (passwd, group string) {
	passwd = fmt.Sprintf("%s:x:%d:%d:%s:/tmp:/sbin/nologin\nnobody:x:65534:65534:nobody:/:/sbin/nologin\n", user, uid, gid, user)
	group = fmt.Sprintf("%s:x:%d:\nnobody:x:65534:\n", user, gid)
	return passwd, group
}

// nssWrapperEnv writes the pair under dir and returns the variables that make
// smbd read it instead of the system's files. The library is the one thing
// this image has to carry for the unprivileged path: when it is missing, the
// image is one built before this path existed (an embedder's, typically), and
// the refusal says which package to add rather than letting smbd die on an
// account it cannot find.
func nssWrapperEnv(dir, user string, uid, gid int) ([]string, error) {
	if _, err := os.Stat(nssWrapperLib); err != nil {
		return nil, fmt.Errorf("running as uid %d needs %s, which this image does not carry: add the nss_wrapper package (Alpine) to the image beside samba-server", uid, nssWrapperLib)
	}
	passwd, group := nssWrapperFiles(user, uid, gid)
	pf, gf := filepath.Join(dir, "passwd"), filepath.Join(dir, "group")
	if err := os.WriteFile(pf, []byte(passwd), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(gf, []byte(group), 0o600); err != nil {
		return nil, err
	}
	return []string{"LD_PRELOAD=" + nssWrapperLib, "NSS_WRAPPER_PASSWD=" + pf, "NSS_WRAPPER_GROUP=" + gf}, nil
}

// mountServe is the argv mode: the container's whole process. smbd runs as its
// child and the container's stop signal is relayed to it, so `docker stop` or
// a pod deletion ends the server cleanly; when smbd dies, so does this, and
// the container's restart policy decides what happens next.
func mountServe() {
	user := os.Getenv(smbUserEnv)
	pass, passFrom, err := smbPassword(os.Getenv(smbPassEnv), os.Getenv(smbPassFileEnv))
	if err != nil {
		fatal("plug-agent mount-serve: %v", err)
	}
	if user == "" || pass == "" {
		fatal("plug-agent mount-serve: %s and %s (or %s) must be set", smbUserEnv, smbPassEnv, smbPassFileEnv)
	}
	share := os.Getenv(smbShareEnv)
	if share == "" {
		share = smbDefaultShare
	}
	allow := strings.Fields(os.Getenv(smbAllowEnv))
	if note := os.Getenv(smbNoteEnv); note != "" {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: note: %s\n", note)
	}
	if _, err := os.Stat(mountVolumePath); err != nil {
		fatal("plug-agent mount-serve: %s is not mounted: %v", mountVolumePath, err)
	}
	listen, err := smbListenPort(os.Getenv(smbListenEnv))
	if err != nil {
		fatal("plug-agent mount-serve: %v", err)
	}
	_, ferr := os.Stat("/usr/lib/samba/vfs/fruit.so")
	o := smbOptions{share: share, user: user, fruit: ferr == nil, allow: allow, listen: listen}
	var (
		uid, gid int
		smbdEnv  []string // what smbd gets on top of this environment
		mode     string   // for the log
	)
	if os.Getuid() == 0 {
		uid, gid = ownerIDs(mountVolumePath)
		// The SMB account is a Unix account to Samba, so it has to exist. It
		// is given the volume owner's ids so that the account and `force user`
		// agree; a root-owned volume is served as root, which needs no account
		// of its own.
		o.forceUser = "root"
		if uid != 0 {
			o.forceUser = user
		}
		if user != "root" {
			if err := ensureUnixUser(user, uid, gid); err != nil {
				fatal("plug-agent mount-serve: creating the %s account: %v", user, err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(smbConfPath), 0o755); err != nil {
			fatal("plug-agent mount-serve: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(smbPasswdPath), 0o700); err != nil {
			fatal("plug-agent mount-serve: %v", err)
		}
		mode = "as root, files written as " + o.forceUser
	} else {
		// Not root (OpenShift's restricted SCC, or anyone running the image
		// with --user): no account to create, no identity to force, and
		// nothing writable but a directory of this process's own.
		uid, gid = os.Getuid(), os.Getgid()
		dir, err := os.MkdirTemp("", "plug-smb-")
		if err != nil {
			fatal("plug-agent mount-serve: %v", err)
		}
		o.stateDir = dir
		for _, d := range smbStateDirs {
			if err := os.MkdirAll(filepath.Join(dir, d[1]), 0o700); err != nil {
				fatal("plug-agent mount-serve: %v", err)
			}
		}
		if smbdEnv, err = nssWrapperEnv(dir, user, uid, gid); err != nil {
			fatal("plug-agent mount-serve: %v", err)
		}
		mode = fmt.Sprintf("as uid %d without privileges, files written as that uid", uid)
	}
	if err := os.WriteFile(o.confFile(), []byte(smbConf(o)), 0o644); err != nil {
		fatal("plug-agent mount-serve: writing %s: %v", o.confFile(), err)
	}
	// The account's NT hash, written straight into Samba's smbpasswd file:
	// no smbpasswd binary (it lives in a package this image does not carry),
	// nothing on a command line, and the password itself never touches disk.
	if err := os.WriteFile(o.passwdFile(), []byte(smbPasswdLine(user, uid, pass, time.Now())), 0o600); err != nil {
		fatal("plug-agent mount-serve: writing %s: %v", o.passwdFile(), err)
	}
	fmt.Fprintf(os.Stderr, "plug-agent mount-serve: serving %s as //%s/%s, listening on :%d and announced on :%d (%s, password from %s)\n",
		mountVolumePath, user, share, listen, smbPort, mode, passFrom)
	if len(allow) > 0 {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: connections accepted from %s only (the agent)\n", strings.Join(allow, ", "))
	} else {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: the agent did not say which addresses it connects from, so no source filter is set\n")
	}
	// -F foreground, no daemon fork, logs on stdout (the container's log).
	smbd := exec.Command("/usr/sbin/smbd", "-F", "--debug-stdout", "--no-process-group", "-s", o.confFile())
	smbd.Stdout, smbd.Stderr = os.Stdout, os.Stderr
	if len(smbdEnv) > 0 {
		smbd.Env = append(os.Environ(), smbdEnv...)
	}
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

// smbPassword picks the credential: the environment when it carries one, else
// the file (a Swarm secret under /run/secrets, which never shows in `docker
// service inspect` the way an Env entry does). Trimmed, because a secret made
// from `echo` ends in a newline and a password with one would never match the
// one the developer's SMB client sends. The second value names the source for
// the log, so a cluster where the secret could not be made (and the agent fell
// back to the environment) is visible from the helper's own log.
func smbPassword(env, file string) (pass, from string, err error) {
	if env != "" {
		return env, "the environment", nil
	}
	if file == "" {
		return "", "", nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", "", fmt.Errorf("reading the password file %s: %v", file, err)
	}
	return strings.TrimSpace(string(b)), "the file " + file, nil
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
