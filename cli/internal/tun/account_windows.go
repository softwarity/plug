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
// is cheap and safe. The check that made it look expensive asked the opposite
// question, who owns the socket behind a port, which on Windows means opening
// another process's token and can fail for reasons that have nothing to do with
// the caller. Asking at registration only ever needs "who am I", which a process
// is never in doubt about.
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

// accountHolds reports whether an account can hold a cluster against another.
// Only a real SID can. The prefix test is what keeps a -1 written by a client
// older than this code, or an empty string from a token that would not read,
// from holding anything against anybody.
func accountHolds(account string) bool {
	return strings.HasPrefix(account, "S-1-") && account != systemSID
}
