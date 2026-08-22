package main

// `plug-agent serve` is the container's only process. It replaces what
// entrypoint.sh and sshd did between them:
//
//	ssh-keygen -A          -> a host key that is KEPT rather than regenerated
//	plug-agent preflight   -> unchanged, and still fatal
//	plug-agent gc          -> unchanged
//	exec sshd -D -e        -> the Go server in sshserver.go
//
// The consequence worth stating: nothing here forks a privileged helper. sshd
// ran the verbs as the unprivileged `plug` user, which is the only reason
// plug-agent had to be setuid root to reach the docker socket or the pod's
// service-account token. This process holds those directly, so the setuid bit
// is gone from the image.

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"
)

const (
	// Where the agent keeps what must outlive a process restart. A directory
	// rather than a file: the host key lives here today, the enrolled keys will
	// join it.
	stateDir = "/opt/plug/state"

	// The keys allowed to open a tunnel. Baked into the image today, which is
	// exactly what /home/plug/.ssh/authorized_keys held: it authenticates the
	// SOFTWARE, not a person. Replacing this file's contents with per-developer
	// keys is the whole of the authentication work, and it changes nothing else.
	authorizedKeysPath = "/opt/plug/authorized_keys"

	// The two accounts' commands, unchanged from the ForceCommand lines they
	// replace. Still separate binaries, still no shell for either.
	tunnelCommand   = "/usr/local/bin/plug-agent"
	downloadCommand = "/usr/local/bin/serve-binary"

	// What ClientAliveInterval 30 / ClientAliveCountMax 3 bought: a tunnel that
	// dies without saying so is noticed within ~90s, which is what frees its
	// remote-forward binds before a reconnecting session tries to re-arm them.
	keepaliveEvery = 30 * time.Second
)

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":22", "address to listen on")
	keys := fs.String("authorized-keys", authorizedKeysPath, "file holding the keys allowed to tunnel")
	state := fs.String("state", stateDir, "directory for state that must outlive a restart")
	_ = fs.Parse(args)

	// Same order as entrypoint.sh, and for the same reasons. preflight is fatal:
	// an agent with no orchestrator access can carry sessions but cannot create
	// a name, and discovering that on someone's first -s hides a missing mount
	// behind a healthy-looking container.
	preflight()

	// An agent (re)start orphans every session's dynamic name, so sweep before
	// serving. Best effort, exactly as the entrypoint had it.
	func() {
		defer func() { _ = recover() }()
		gc()
	}()

	authorized, err := os.ReadFile(*keys)
	if err != nil {
		fatal("plug-agent: cannot read %s: %v", *keys, err)
	}
	parsed, err := parseAuthorizedKeys(authorized)
	if err != nil {
		fatal("plug-agent: %v", err)
	}

	hostKey, err := hostKeySigner(*state + "/host_key")
	if err != nil {
		fatal("plug-agent: host key: %v", err)
	}

	srv := &sshServer{
		keys:     embeddedKeys{authorized: parsed},
		hostKey:  hostKey,
		idleEvry: keepaliveEvery,
		execFor: func(user string) []string {
			switch user {
			case tunnelUser:
				return []string{tunnelCommand}
			case downloadUser:
				return []string{downloadCommand}
			}
			return nil
		},
		logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "plug-agent: "+format+"\n", a...)
		},
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal("plug-agent: listen %s: %v", *addr, err)
	}
	version, _ := os.ReadFile("/opt/plug/VERSION")
	fmt.Printf("plug agent ready (v%s) on %s, %d authorized key(s)\n",
		trimSpace(string(version)), *addr, len(parsed))

	if err := srv.Serve(ln); err != nil {
		fatal("plug-agent: serve: %v", err)
	}
}

// trimSpace without pulling strings in for one call site in a startup message.
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\r' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 {
		if c := s[len(s)-1]; c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}
