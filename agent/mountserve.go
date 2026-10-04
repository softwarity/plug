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
// There is ONE shape, on every backend. The helper runs without privilege,
// under the uid and gid of the workload whose volume it serves: the agent reads
// them on the workload's process and starts the helper with them (mount.go,
// helperIDs), so what the developer saves is what the workload can read back,
// and the pod is one a restricted admission (OpenShift's SCC, Pod Security)
// lets in. Nothing here needs root: smbd listens on smbListenPort, above 1024,
// keeps every file it opens for itself under a directory of its own, and
// resolves the SMB account through nss_wrapper (nssWrapperFiles), since the
// uid it runs as is in no /etc/passwd. No Unix account is created and no
// identity is forced: files are written by the process smbd is.
//
// One fallback, when this process is root: the agent could not tell the
// workload's uid and the image runs as root, or the workload is root itself.
// Then smbd is started under the uid and gid that own the volume's root, the
// next best answer to "who writes here"; a volume owned by root is served as
// root.
const (
	mountVolumePath = "/mnt/vol"
	smbUserEnv      = "PLUG_SMB_USER"
	smbPassEnv      = "PLUG_SMB_PASS"
	smbPassFileEnv  = "PLUG_SMB_PASS_FILE" // the password's file, when it is not in the environment (Swarm secret)
	smbShareEnv     = "PLUG_SMB_SHARE"
	smbAllowEnv     = "PLUG_SMB_ALLOW" // space-separated addresses that may connect; empty means no filter
	smbNoteEnv      = "PLUG_SMB_NOTE"  // one line the agent wants in the helper's log (a fallback it took)
	smbDefaultShare = "vol"
	// smbListenPort is the port smbd binds, everywhere: above 1024, so no
	// capability and no sysctl is needed to open it. The client is told 445
	// (mountHelperPort); a Service maps one to the other on Kubernetes, the
	// agent does when it dials on Docker and Swarm (mountDialPort).
	smbListenPort = 1445
)

// nssWrapperLib is where Alpine's nss_wrapper package puts the library smbd is
// preloaded with. A variable so a test can point it at nothing and read the
// refusal.
var nssWrapperLib = "/usr/lib/libnss_wrapper.so"

// smbOptions is everything smbConf renders: the share and its account, the
// addresses allowed in, and the directory every file of Samba's goes under
// (its compiled-in ones, /var/lib/samba and the like, are root's).
type smbOptions struct {
	share, user string
	fruit       bool
	allow       []string
	stateDir    string
}

// smbStateDirs are the directories smbd opens for itself: each Samba option
// and the subdirectory it is given under stateDir. Created before smbd starts,
// since smbd makes some of them on its own and not others.
var smbStateDirs = [][2]string{
	{"lock directory", "lock"}, {"state directory", "state"}, {"cache directory", "cache"},
	{"private dir", "private"}, {"pid directory", "pid"}, {"ncalrpc dir", "ncalrpc"},
}

func (o smbOptions) passwdFile() string { return filepath.Join(o.stateDir, "smbpasswd") }
func (o smbOptions) confFile() string   { return filepath.Join(o.stateDir, "smb.conf") }

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
// No `force user`: smbd already runs as the identity files are written under.
func smbConf(o smbOptions) string {
	var b strings.Builder
	b.WriteString("[global]\n")
	b.WriteString("  security = user\n")
	b.WriteString("  map to guest = Never\n")
	b.WriteString("  passdb backend = smbpasswd\n")
	// Every path smbd opens for itself, under the one directory it owns.
	b.WriteString("  smb passwd file = " + o.passwdFile() + "\n")
	for _, d := range smbStateDirs {
		b.WriteString("  " + d[0] + " = " + filepath.Join(o.stateDir, d[1]) + "\n")
	}
	b.WriteString("  log file = " + filepath.Join(o.stateDir, "log.smbd") + "\n")
	b.WriteString("  server min protocol = SMB2_02\n")
	b.WriteString("  server signing = mandatory\n")
	b.WriteString("  smb encrypt = desired\n")
	if len(o.allow) > 0 {
		b.WriteString("  hosts allow = " + strings.Join(o.allow, " ") + "\n")
	}
	b.WriteString("  smb ports = " + strconv.Itoa(smbListenPort) + "\n")
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
	b.WriteString("  create mask = 0664\n")
	b.WriteString("  directory mask = 0775\n")
	return b.String()
}

// nssWrapperFiles is the passwd and group pair nss_wrapper serves smbd. Samba
// turns the SMB account into a Unix one through NSS, and the uid the helper
// is given is in no /etc/passwd; and it wants the guest account (`nobody`) to
// exist at start even though `map to guest = Never` means no connection ever
// becomes it. Two users, two groups, nothing else: a lookup of any other uid
// (a file on the volume owned by someone else) fails and Samba maps it to a
// Unix-users SID, which is fine for a share nobody browses by owner.
func nssWrapperFiles(user string, uid, gid int) (passwd, group string) {
	passwd = fmt.Sprintf("%s:x:%d:%d:%s:/tmp:/sbin/nologin\nnobody:x:65534:65534:nobody:/:/sbin/nologin\n", user, uid, gid, user)
	group = fmt.Sprintf("%s:x:%d:\nnobody:x:65534:\n", user, gid)
	return passwd, group
}

// nssWrapperEnv writes the pair under dir and returns the variables that make
// smbd read it instead of the system's files. The library is the one thing the
// image has to carry beside Samba: when it is missing, the image is one built
// before the helper ran unprivileged (an embedder's, typically), and the
// refusal says which package to add rather than letting smbd die on an account
// it cannot find.
func nssWrapperEnv(dir, user string, uid, gid int) ([]string, error) {
	if _, err := os.Stat(nssWrapperLib); err != nil {
		return nil, fmt.Errorf("the helper needs %s, which this image does not carry: add the nss_wrapper package (Alpine) to the image beside samba-server", nssWrapperLib)
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

// smbdIdentity is who smbd runs as, hence who owns what the developer writes,
// and the words the log has for where that came from. A process that is not
// root is what the agent made it: the workload's uid and gid. A root process
// means the agent could not tell (and the image runs as root) or the workload
// is root, so the owner of the volume's root decides; change says smbd has to be started under another
// identity than this process's. A root-owned volume, or one that cannot be
// read, is served as root.
func smbdIdentity(procUID, procGID int, owner func() (uid, gid int, err error)) (uid, gid int, from string, change bool) {
	if procUID != 0 {
		return procUID, procGID, "the workload's, given by the agent", false
	}
	ouid, ogid, err := owner()
	if err != nil || ouid == 0 {
		return procUID, procGID, "the volume owner's", false
	}
	return ouid, ogid, "the volume owner's", true
}

// startSmbd writes Samba's whole state for one identity (the configuration,
// the account's hash, the passwd and group nss_wrapper serves) and starts smbd
// in the foreground under it. as says the identity is not this process's: the
// state directory is handed over first, since smbd must own what it opens.
func startSmbd(o smbOptions, pass string, uid, gid int, as bool) (*exec.Cmd, error) {
	env, err := nssWrapperEnv(o.stateDir, o.user, uid, gid)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(o.confFile(), []byte(smbConf(o)), 0o644); err != nil {
		return nil, err
	}
	// The account's NT hash, written straight into Samba's smbpasswd file:
	// no smbpasswd binary (it lives in a package this image does not carry),
	// nothing on a command line, and the password itself never touches disk.
	if err := os.WriteFile(o.passwdFile(), []byte(smbPasswdLine(o.user, uid, pass, time.Now())), 0o600); err != nil {
		return nil, err
	}
	// -F foreground, no daemon fork, logs on stdout (the container's log).
	smbd := exec.Command("/usr/sbin/smbd", "-F", "--debug-stdout", "--no-process-group", "-s", o.confFile())
	smbd.Stdout, smbd.Stderr = os.Stdout, os.Stderr
	smbd.Env = append(os.Environ(), env...)
	if as {
		if err := chownTree(o.stateDir, uid, gid); err != nil {
			return nil, err
		}
		runAs(smbd, uid, gid)
	}
	return smbd, smbd.Start()
}

// chownTree hands a directory and everything under it to uid:gid, the
// directories last: a root without DAC_OVERRIDE no longer enters a 0700
// directory once it is someone else's.
func chownTree(dir string, uid, gid int) error {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		paths = append(paths, p)
		return err
	})
	for i := len(paths) - 1; i >= 0 && err == nil; i-- {
		err = os.Chown(paths[i], uid, gid)
	}
	return err
}

// smbStateDir makes the directory all of Samba's state goes under, with the
// subdirectories smbd expects to find.
func smbStateDir() (string, error) {
	dir, err := os.MkdirTemp("", "plug-smb-")
	if err != nil {
		return "", err
	}
	for _, d := range smbStateDirs {
		if err := os.MkdirAll(filepath.Join(dir, d[1]), 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
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
	dir, err := smbStateDir()
	if err != nil {
		fatal("plug-agent mount-serve: %v", err)
	}
	_, ferr := os.Stat("/usr/lib/samba/vfs/fruit.so")
	o := smbOptions{share: share, user: user, fruit: ferr == nil, allow: allow, stateDir: dir}
	procUID, procGID := os.Getuid(), os.Getgid()
	uid, gid, from, change := smbdIdentity(procUID, procGID, func() (int, int, error) { return ownerIDs(mountVolumePath) })
	smbd, err := startSmbd(o, pass, uid, gid, change)
	if err != nil && change {
		// Root in name only: the container was not left the capabilities to
		// become someone else (CHOWN, SETUID, SETGID). Served as the process
		// it is, then, and said, since files will not be the owner's.
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: cannot run smbd as %d:%d, the owner of %s (%v): staying %d:%d\n",
			uid, gid, mountVolumePath, err, procUID, procGID)
		uid, gid, from = procUID, procGID, "this process's own, the volume owner's being out of reach"
		// A directory of its own again: the first may be half handed over.
		if o.stateDir, err = smbStateDir(); err == nil {
			smbd, err = startSmbd(o, pass, uid, gid, false)
		}
	}
	if err != nil {
		fatal("plug-agent mount-serve: starting smbd: %v", err)
	}
	fmt.Fprintf(os.Stderr, "plug-agent mount-serve: serving %s as //%s/%s on :%d, files written as %d:%d (%s), password from %s\n",
		mountVolumePath, user, share, smbListenPort, uid, gid, from, passFrom)
	if len(allow) > 0 {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: connections accepted from %s only (the agent)\n", strings.Join(allow, ", "))
	} else {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: the agent did not say which addresses it connects from, so no source filter is set\n")
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
