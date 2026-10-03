//go:build windows

package tun

import (
	"strings"

	"golang.org/x/sys/windows"
)

// An ACCOUNT is who a cluster belongs to. On Windows that is the SID of the
// token this process runs under, and it exists here because os.Getuid returns -1
// for every process on this platform: until now every client recorded the same
// owner, so no account could be told from another and the rule had nothing to
// compare. That is why a second session on this machine could reach a cluster
// somebody else had opened, with their key, while macOS refused it.
//
// The SID is read off THIS process's own token, which is the whole reason this
// is cheap and safe: asking at registration only ever needs "who am I", which a
// process is never in doubt about. The other direction, who runs the process
// behind a port, is accountOfPID below, and it carries the caveats this one
// does not.
//
// An empty string on failure, and accountHolds rejects it: an identity that could
// not be read must not become an identity that holds a cluster, in either
// direction.
func thisAccount() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || u == nil || u.User.Sid == nil {
		return ""
	}
	return u.User.Sid.String()
}

// systemSID is LocalSystem, the account the plug service runs as. It is exempted
// for the same reason root is on macOS: it already owns the machine, and the
// service's own work must not be refused by the rule the service enforces.
const systemSID = "S-1-5-18"

// administratorsSID is BUILTIN\Administrators. A file created by an ELEVATED
// process is owned by this group rather than by the person's own SID, which is
// what a plug launched from an administrator console leaves behind. Whoever
// can write as Administrators already owns the machine, so a marker owned by
// it is trusted the way LocalSystem's would be.
const administratorsSID = "S-1-5-32-544"

// accountHolds reports whether an account can hold a cluster against another.
// Only a real SID can. The prefix test is what keeps a -1 written by a client
// older than this code, or an empty string from a token that would not read,
// from holding anything against anybody.
func accountHolds(account string) bool {
	return strings.HasPrefix(account, "S-1-") && account != systemSID
}

// accountOfPID is the account another process runs under: the user SID of its
// primary token. It answers two questions. On the SYN of every new connection
// in a single-cluster session, soleAllows asks whether the socket's owner has
// a client on that cluster, and until this existed the Windows answer was
// "unknown", which that check reads as "allowed": every account on the box
// reached whatever cluster was up. And the registry asks it of the pid a
// marker names, to compare against the file's owner (markerOwnedByProcess).
//
// PROCESS_QUERY_LIMITED_INFORMATION is enough to open the token for TOKEN_QUERY
// since Vista, and the plug service runs as LocalSystem, which can open any
// ordinary process. A pid that has exited, or a token that will not open,
// reports failure: soleAllows lets that through, as it does for a pid the
// socket table could not name, because a datapath that starts refusing on a
// bad second is worse than the leak it closes.
func accountOfPID(pid int) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", false
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil || u == nil || u.User.Sid == nil {
		return "", false
	}
	return u.User.Sid.String(), true
}

// fileOwnerSID is the SID NTFS recorded as the owner of path. Windows records
// the creator of a file and no caller can make it name somebody else, which is
// what makes the owner worth comparing against anything a user may have
// written inside the file.
func fileOwnerSID(path string) (string, bool) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return "", false
	}
	return owner.String(), true
}

// markerOwnedByProcess reports whether the registry file at path was written
// by the account that process pid runs under.
//
// The registry directory, %ProgramData%\plug, is writable by every account on
// the machine, on purpose: a launcher registers without elevation. So the NAME
// of a marker (a pid) and what it SAYS (a cluster key) are both the writer's to
// choose, and a file named with somebody else's pid would route that person's
// flows into the writer's cluster. The owner is the one thing the writer does
// not choose. A marker owned by LocalSystem or by Administrators is trusted
// outright: whoever writes as those owns the machine already.
//
// Unknown is not a refusal, as everywhere in this registry: an owner or a
// token that cannot be read answers yes, and the stamp and the directory name
// still have to hold. The service, which is the reader that matters, runs as
// LocalSystem and reads both; it is the non-elevated launcher, asking
// ClusterHeldByOther about another account's process, that may be refused
// the token, and refusing the marker there would switch that rule off.
func markerOwnedByProcess(path string, pid int) bool {
	owner, ok := fileOwnerSID(path)
	if !ok {
		return true
	}
	if owner == systemSID || owner == administratorsSID {
		return true
	}
	who, ok := accountOfPID(pid)
	if !ok {
		return true
	}
	return owner == who
}
