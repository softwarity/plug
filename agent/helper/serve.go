package agent

// Start is the agent, as a function. It is what `plug-agent serve` runs in the
// container, and what Meerkat calls in a goroutine when it links the agent into
// its own binary.
//
// It replaces what entrypoint.sh and sshd did between them:
//
//	ssh-keygen -A          -> an identity the Host keeps
//	plug-agent preflight   -> returned as an error, not fatal (see below)
//	plug-agent gc          -> unchanged
//	exec sshd -D -e        -> the Go server in sshserver.go
//
// Nothing here forks a privileged helper. sshd ran the verbs as the
// unprivileged `plug` user, which is the only reason plug-agent had to be
// setuid root to reach the docker socket or the SA token. This process holds
// them directly, so the setuid bit is gone from the image.
//
// Errors are returned rather than fatal, and that is the whole point of this
// file. A standalone agent SHOULD die when it cannot provision names: a healthy
// container that fails on someone's first -s hides a missing mount. A gateway
// must not: a forgotten RBAC rule would stop an otherwise working Meerkat from
// starting. Same code, and the caller decides.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const (
	// Where a standalone agent keeps what must outlive a process restart. Under
	// Meerkat the Host answers instead, from its vault.
	stateDir = "/opt/plug/state"

	// The keys a standalone agent accepts. Baked into the image, exactly as
	// /home/plug/.ssh/authorized_keys was: it authenticates the SOFTWARE, not a
	// person.
	authorizedKeysPath = "/opt/plug/authorized_keys"

	// The download account's command. Still a separate program, still no shell.
	downloadCommand = "/usr/local/bin/serve-binary"

	// What ClientAliveInterval 30 / ClientAliveCountMax 3 bought: a tunnel that
	// dies without saying so is noticed within ~90s, which frees its
	// remote-forward binds before a reconnecting session tries to re-arm them.
	keepaliveEvery = 30 * time.Second
)

// Config is what the caller supplies. Every field has a working default, so an
// embedder that only sets Host gets the standalone behaviour.
type Config struct {
	// Host answers the three things the agent cannot decide alone. Required.
	Host Host

	// Addr to listen on. Defaults to :22.
	Addr string

	// VerbCommand runs a verb (serve-name, resolve, …) with the request in
	// SSH_ORIGINAL_COMMAND. It is a SUBPROCESS on purpose: the verbs call
	// os.Exit on error, which is correct for a process whose whole job is one
	// answer, and unacceptable inside a gateway.
	//
	// Standalone this is the agent binary. Meerkat sets it to its OWN executable
	// plus a hidden argument, and re-execs itself the way plug's launcher does.
	// Defaults to os.Args[0] with no argument.
	VerbCommand []string

	// DownloadCommand serves the CLI binaries to the anonymous account.
	// Defaults to /usr/local/bin/serve-binary.
	DownloadCommand []string

	// Logf receives one line per notable event. Defaults to stderr.
	Logf func(string, ...any)

	// RequireOrchestrator makes a missing docker socket or RBAC fatal. True for
	// a standalone agent, false for an embedder that has other work to do.
	RequireOrchestrator bool
}

// Start serves until ctx is cancelled or the listener fails.
func Start(ctx context.Context, cfg Config) error {
	if cfg.Host == nil {
		return errors.New("plug agent: no Host supplied")
	}
	if cfg.Addr == "" {
		cfg.Addr = ":22"
	}
	if len(cfg.VerbCommand) == 0 {
		cfg.VerbCommand = []string{os.Args[0]}
	}
	if len(cfg.DownloadCommand) == 0 {
		cfg.DownloadCommand = []string{downloadCommand}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "plug-agent: "+format+"\n", a...)
		}
	}

	// Same order as the entrypoint it replaces, and for the same reasons.
	if err := Preflight(); err != nil {
		if cfg.RequireOrchestrator {
			return err
		}
		// The embedder keeps running: consuming the cluster still works, only
		// name provisioning (-s) does not. Said out loud rather than discovered
		// on someone's first attempt.
		cfg.Logf("names cannot be provisioned: %v", err)
	}

	// An agent (re)start orphans every session's dynamic name, so sweep before
	// serving.
	gcQuietly()

	hostKey, err := cfg.Host.HostKey()
	if err != nil {
		return fmt.Errorf("host key: %w", err)
	}

	srv := &sshServer{
		host:     cfg.Host,
		hostKey:  hostKey,
		idleEvry: keepaliveEvery,
		execFor: func(user string) []string {
			switch user {
			case tunnelUser:
				return cfg.VerbCommand
			case downloadUser:
				return cfg.DownloadCommand
			}
			return nil
		},
		logf: cfg.Logf,
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Addr, err)
	}
	// Shutdown in two steps, and the second is not optional: closing the
	// listener stops new connections, but Serve waits for the live ones, so an
	// embedder would hang on whoever left a session open.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		srv.CloseConnections()
	}()

	version, _ := os.ReadFile("/opt/plug/VERSION")
	cfg.Logf("ready (v%s) on %s", strings.TrimSpace(string(version)), cfg.Addr)

	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// gcQuietly sweeps orphaned names, best effort. The verbs it reaches call
// os.Exit on their own failures, which a caller embedding this package must not
// inherit.
func gcQuietly() {
	defer func() { _ = recover() }()
	gc()
}

// Standalone returns the Host plug uses without a gateway: the keys baked into
// the image, and an identity kept in a file only root can read.
func Standalone(authorizedKeysFile, stateDirectory string) (Host, error) {
	if authorizedKeysFile == "" {
		authorizedKeysFile = authorizedKeysPath
	}
	if stateDirectory == "" {
		stateDirectory = stateDir
	}
	b, err := os.ReadFile(authorizedKeysFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", authorizedKeysFile, err)
	}
	keys, err := parseAuthorizedKeys(b)
	if err != nil {
		return nil, err
	}
	return &standaloneHost{authorized: keys, keyPath: stateDirectory + "/host_key"}, nil
}

// serve is the `plug-agent serve` verb: Standalone plus Start, with a missing
// orchestrator fatal because that is right for a dedicated agent.
func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":22", "address to listen on")
	keys := fs.String("authorized-keys", authorizedKeysPath, "file holding the keys allowed to tunnel")
	state := fs.String("state", stateDir, "directory for state that must outlive a restart")
	_ = fs.Parse(args)

	host, err := Standalone(*keys, *state)
	if err != nil {
		fatal("plug-agent: %v", err)
	}
	err = Start(context.Background(), Config{
		Host:                host,
		Addr:                *addr,
		RequireOrchestrator: true,
	})
	if err != nil {
		fatal("plug-agent: %v", err)
	}
}
