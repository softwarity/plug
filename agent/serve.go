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
	//
	// Outside /opt/plug on purpose: that directory is what the agent HANDS OUT,
	// and an embedder copies it wholesale into its own image. A host key and an
	// admission list travelling there are read as meaningful when they are not.
	stateDir = "/var/lib/plug/state"

	// The keys a standalone agent accepts. Baked into the image, exactly as
	// /home/plug/.ssh/authorized_keys was: it authenticates the SOFTWARE, not a
	// person. Beside the state, for the same reason.
	authorizedKeysPath = "/var/lib/plug/authorized_keys"

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

	// Version is what the `info` verb reports: the version `plug doctor` shows
	// and the one `plug update` resolves a target against. Defaults to
	// /opt/plug/VERSION, which exists in the plug image and nowhere else.
	//
	// It is NOT the whole story, and the other half is easy to miss. The
	// LAUNCHER asks the download account instead (`version` through
	// DownloadCommand), turns that answer into a cache path, then asks the same
	// account for that build's digest and refuses to run a core it cannot
	// verify. So an embedder must make DownloadCommand answer `version` too, and
	// answer it with THIS string: two different answers means a client caching
	// one version and verifying against another.
	Version string

	// SignpostImage is the image the signpost container runs on Compose and
	// Swarm. It must carry the plug binary, since the entrypoint is
	// /usr/local/bin/plug-agent. Defaults to the agent's OWN image, which is
	// right when the agent is the plug image and wrong for an embedder: a
	// gateway would build a signpost from its own image and the container would
	// die on a missing binary. Kubernetes never uses this (it points a Service
	// at the agent and creates no pod).
	SignpostImage string

	// NoSelfUpdate refuses the self-update verb. Set it in a gateway: the verb
	// rewrites the image of the deployment the agent runs in, which is the
	// gateway's own, and it is reachable by anyone who reaches the port.
	NoSelfUpdate bool

	// NoDownloadAccount removes the anonymous `get` account. That account has no
	// authentication by design (requiring a key to fetch the thing that holds
	// the key is a circle) and is bounded only by its fixed command. It is how a
	// developer installs and how the launcher fetches the core matching this
	// cluster, so removing it means the embedder distributes plug some other
	// way. Said out loud because it is a surface, not a detail.
	NoDownloadAccount bool
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
	if cfg.Version == "" {
		if b, err := os.ReadFile(versionFile); err == nil {
			cfg.Version = strings.TrimSpace(string(b))
		}
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

	// Two things an embedder gets wrong silently, said at boot rather than
	// discovered on somebody's first run. Neither stops the agent: an embedder
	// may be serving binaries some other way, or may not want -s on Swarm.
	if cfg.Version == "" {
		cfg.Logf("no version to report: the CLI turns that answer into a cache path and " +
			"will refuse to run a core it cannot verify. Set Config.Version")
	}
	if !cfg.NoDownloadAccount && len(cfg.DownloadCommand) == 1 && cfg.DownloadCommand[0] == downloadCommand {
		if _, err := os.Stat(downloadCommand); err != nil {
			cfg.Logf("%s is not here, so the download account can serve nothing. That account is "+
				"where the LAUNCHER reads this cluster's version and the digest of the core it "+
				"then runs, so clients cannot install and cannot start at all. Set "+
				"Config.DownloadCommand (it must answer `version` with %q, `install`, "+
				"`<os>-<arch>` and `wintun`), or Config.NoDownloadAccount to close it on purpose",
				downloadCommand, cfg.Version)
		}
	}

	// An agent (re)start orphans every session's dynamic name, so sweep before
	// serving.
	gcQuietly(cfg.Logf)

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
		// What a verb subprocess cannot ask the Host for, because it runs in
		// another process: everything the embedder decided.
		verbEnv: []string{
			versionEnv + "=" + cfg.Version,
			signpostImageEnv + "=" + cfg.SignpostImage,
			noSelfUpdateEnv + "=" + boolEnv(cfg.NoSelfUpdate),
		},
		noDownloadAccount: cfg.NoDownloadAccount,
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

	cfg.Logf("ready (v%s) on %s", cfg.Version, cfg.Addr)

	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// gcQuietly sweeps orphaned names, best effort. The verbs it reaches call
// os.Exit on their own failures, which a caller embedding this package must not
// inherit.
//
// Best effort, and SAID. This sweep is what brings back what a previous session
// parked: containers restarted, a Swarm service scaled back, a Kubernetes
// Service repointed. Swallowing its panic left an agent announcing "ready" one
// line later while somebody's deployment stayed at zero replicas, with nothing
// anywhere naming the sweep that never finished. That is the failure the parking
// receipt exists to prevent, arriving through the door meant to protect the
// embedder.
func gcQuietly(logf func(string, ...any)) {
	runQuietly("the boot sweep", logf, gc)
}

// runQuietly absorbs a panic and names it. Extracted so the absorbing can be
// tested: the thing it wraps calls the orchestrator and cannot be exercised from
// a unit test, but the wrapper's contract - never propagate, always report - can.
func runQuietly(what string, logf func(string, ...any), f func()) {
	defer func() {
		if r := recover(); r != nil && logf != nil {
			logf("%s did not finish (%v). A workload parked by an earlier session may still be stopped; "+
				"re-run that session to restore it, or restart this agent", what, r)
		}
	}()
	f()
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
