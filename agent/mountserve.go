package agent

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/softwarity/smbserver"
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
// There is ONE shape, on every backend. The server is compiled into this
// binary (github.com/softwarity/smbserver): no package beside it in the image,
// no configuration or state on disk, no account to resolve, no child process.
// The helper runs without privilege, under the uid and gid of the workload
// whose volume it serves: the agent reads them on the workload's process and
// starts the helper with them (mount.go, helperIDs), so what the developer
// saves is what the workload can read back, and the pod is one a restricted
// admission (OpenShift's SCC, Pod Security) lets in. Files are written by the
// process this is, whatever its uid, and it listens on smbListenPort, above
// 1024.
//
// One fallback, when this process is root: the agent could not tell the
// workload's uid and the image runs as root, or the workload is root itself.
// Then the process becomes the uid and gid that own the volume's root, the
// next best answer to "who writes here", before it serves; a volume owned by
// root is served as root.
const (
	mountVolumePath = "/mnt/vol"
	smbUserEnv      = "PLUG_SMB_USER"
	smbPassEnv      = "PLUG_SMB_PASS"
	smbPassFileEnv  = "PLUG_SMB_PASS_FILE" // the password's file, when it is not in the environment (Swarm secret)
	smbShareEnv     = "PLUG_SMB_SHARE"
	smbAllowEnv     = "PLUG_SMB_ALLOW" // space-separated addresses that may connect; empty means no filter
	smbNoteEnv      = "PLUG_SMB_NOTE"  // one line the agent wants in the helper's log (a fallback it took)
	smbDefaultShare = "vol"
	// smbListenPort is the port the server binds, everywhere: above 1024, so
	// no capability and no sysctl is needed to open it. The client is told 445
	// (mountHelperPort); a Service maps one to the other on Kubernetes, the
	// agent does when it dials on Docker and Swarm (mountDialPort).
	smbListenPort = 1445
)

// serveIdentity is who the server runs as, hence who owns what the developer
// writes, and the words the log has for where that came from. A process that
// is not root is what the agent made it: the workload's uid and gid. A root
// process means the agent could not tell (and the image runs as root) or the
// workload is root, so the owner of the volume's root decides; change says the
// process has to become another identity than its own. A root-owned volume, or
// one that cannot be read, is served as root.
func serveIdentity(procUID, procGID int, owner func() (uid, gid int, err error)) (uid, gid int, from string, change bool) {
	if procUID != 0 {
		return procUID, procGID, "the workload's, given by the agent", false
	}
	ouid, ogid, err := owner()
	if err != nil || ouid == 0 {
		return procUID, procGID, "the volume owner's", false
	}
	return ouid, ogid, "the volume owner's", true
}

// allowPrefixes turns the agent's addresses and subnets into what the server
// filters on. allow is the list of addresses a connection may come from: the
// AGENT's, and nothing else, because the developer's SMB client never reaches
// this port itself. It rides a direct-tcpip channel to the agent, and the
// agent dials the helper, so every legitimate connection carries the agent's
// address as its source. Anything else on the application network (a workload,
// a neighbour's container) is refused before it can try the password. An empty
// list sets NO filter: an agent unsure of its addresses must not turn a
// working mount into a refusal, it says so in the log instead. An entry that
// does not parse is a refusal to start, not an entry skipped: skipped down to
// none, the list would mean "anyone".
func allowPrefixes(allow []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, a := range allow {
		if p, err := netip.ParsePrefix(a); err == nil {
			out = append(out, p.Masked())
			continue
		}
		ip, err := netip.ParseAddr(a)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is neither an address nor a subnet", smbAllowEnv, a)
		}
		out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
	}
	return out, nil
}

// mountServe is the argv mode: the container's whole process. The container's
// stop signal ends the server cleanly, so `docker stop` or a pod deletion does;
// when the server fails, so does this, and the container's restart policy
// decides what happens next.
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
	prefixes, err := allowPrefixes(allow)
	if err != nil {
		fatal("plug-agent mount-serve: %v", err)
	}
	if note := os.Getenv(smbNoteEnv); note != "" {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: note: %s\n", note)
	}
	if _, err := os.Stat(mountVolumePath); err != nil {
		fatal("plug-agent mount-serve: %s is not mounted: %v", mountVolumePath, err)
	}
	// Listen first: binding needs nothing the new identity lacks, and a port
	// already taken is said before anything is given up.
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(smbListenPort))
	if err != nil {
		fatal("plug-agent mount-serve: %v", err)
	}
	procUID, procGID := os.Getuid(), os.Getgid()
	uid, gid, from, change := serveIdentity(procUID, procGID, func() (int, int, error) { return ownerIDs(mountVolumePath) })
	if change {
		if err := becomeIDs(uid, gid); err != nil {
			// Root in name only: the container was not left the capabilities
			// to become someone else (SETUID, SETGID). Served as the process
			// it is, then, and said, since files will not be the owner's.
			fmt.Fprintf(os.Stderr, "plug-agent mount-serve: cannot become %d:%d, the owner of %s (%v): staying %d:%d\n",
				uid, gid, mountVolumePath, err, os.Getuid(), os.Getgid())
			uid, gid, from = os.Getuid(), os.Getgid(), "this process's own, the volume owner's being out of reach"
		}
	}
	fmt.Fprintf(os.Stderr, "plug-agent mount-serve: serving %s as //%s/%s on :%d, files written as %d:%d (%s), password from %s\n",
		mountVolumePath, user, share, smbListenPort, uid, gid, from, passFrom)
	if len(allow) > 0 {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: connections accepted from %s only (the agent)\n", strings.Join(allow, ", "))
	} else {
		fmt.Fprintf(os.Stderr, "plug-agent mount-serve: the agent did not say which addresses it connects from, so no source filter is set\n")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = smbserver.Serve(ctx, ln, smbserver.Config{
		Root: mountVolumePath, Share: share, User: user, NTHash: smbserver.NTHash(pass), Allow: prefixes,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "plug-agent mount-serve: "+format+"\n", args...)
		},
	})
	if err != nil {
		fatal("plug-agent mount-serve: %v", err)
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
