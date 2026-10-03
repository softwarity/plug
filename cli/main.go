package main

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

//go:embed keys/id_ed25519
var embeddedKey []byte

// version is stamped at build time via -ldflags "-X main.version=x.y.z".
var version = "dev"

const sshUser = "plug" // tunnel user (public-key)
const getUser = "get"  // download user (passwordless, ForceCommand)
const defaultPort = "2222"

type config struct {
	host string
	// envPolicy is what --no-env said about projecting the workload's
	// environment onto the command; the zero value projects everything.
	envPolicy envPolicy
	port      string
	exposes   []tunnel.ExposeSpec
	// mounts are the --mount volumes, resolved: mounted before the command
	// runs, unmounted after it (mount.go). mountPolicy is what --no-mount said
	// about the AUTOMATIC ones; the zero value mounts every data volume.
	mounts      []mountSpec
	mountPolicy mountPolicy
	// dockerRun: the mounts go to a container (cifs volumes), not to this host.
	dockerRun bool
	// updateMode is the cluster's update policy (none|notify|auto). It belongs
	// to the profile because `auto` updates the AGENT, which is shared: you may
	// govern your own cluster and have no say over the shared one.
	updateMode string
	// key is the path to this profile's PERSONAL private key, written by
	// `plug keygen`. Empty means the profile has none and the binary's built-in
	// key is the only identity, which is every profile until someone runs keygen.
	// It is per profile on purpose: one identity per cluster is what an operator
	// enrols and revokes, and a single key shared across clusters could not be
	// withdrawn from one of them.
	key string
}

// authKeys is what plug offers the agent, in order: this profile's personal key
// when it has one, then the key built into the binary.
//
// Both, never one or the other. An agent that does not check keys accepts the
// embedded one and a personal key would be an unexplained refusal; an agent that
// does accepts the personal one and ignores the rest. Offering the pair is what
// lets `plug keygen` be run at any time, against any cluster, without a flag day.
//
// authKeys is the entry-point face: a key that cannot be offered ends the
// command. authKeysErr is the same decision as a value, for the daemon, the
// MCP server and everything that dials through dialTunnel (see refusal.go).
func (c config) authKeys() [][]byte {
	keys, err := c.authKeysErr()
	if err != nil {
		fatal("%v", err)
	}
	return keys
}

func (c config) authKeysErr() ([][]byte, error) {
	if c.key == "" {
		return [][]byte{embeddedKey}, nil
	}
	// The core and the daemon read this while holding a privilege the caller does
	// not, and the path arrives through the environment. Same rule as every other
	// privileged path under the user's home: only where they could have read it
	// themselves. Without the guard, PLUG_CORE_KEY=/etc/… turns plug into an
	// oracle for whether a root-only file exists and parses as a key.
	if err := userPathError(c.key); err != nil {
		return nil, err
	}
	if err := keyPathError(c.key); err != nil {
		return nil, err
	}
	personal, err := readUserOwnedFile(c.key)
	if err != nil {
		return nil, refuse("profile key %s: %v\n"+
			"      that path comes from `key = ...` in the profile. Regenerate it with\n"+
			"      'plug keygen', or delete the line to fall back to the built-in key", c.key, err)
	}
	return [][]byte{personal, embeddedKey}, nil
}

// authKeyNames describes, in the same order, what authKeys offers. Only ever
// used to say WHICH key an agent refused: the refusal names a fingerprint, and a
// fingerprint the person cannot place is the difference between a ten-second fix
// and an afternoon.
func (c config) authKeyNames() []string {
	if c.key == "" {
		return []string{"the key built into plug"}
	}
	return []string{"this profile's key (" + c.key + ")", "the key built into plug"}
}

func main() {
	// Before anything else: plug may hold a privilege the caller does not, and
	// everything below can exec a system helper by name. See securePath.
	securePath()
	// Mount-namespace shim: a re-exec of ourselves (inside the child's new mount
	// ns) that bind-mounts its private resolv.conf and execs the real command.
	// Checked first — it inherits PLUG_CORE=1 and must not fall into coreMain.
	if len(os.Args) > 1 && os.Args[1] == tun.NsShimVerb {
		if err := tun.NsShimMain(os.Args[2:]); err != nil {
			fatal("%v", err)
		}
		return
	}
	// The persistent macOS datapath daemon: a detached re-exec that holds the
	// datapath for one cluster (see daemonMain). Checked before PLUG_CORE.
	//
	// Unguarded, and deliberately so, unlike its neighbour above. An audit paired
	// the two as "the same unauthenticated primitive"; they are not. The shim
	// above performs a mount with a caller-named file, and gating it closed a real
	// hole. This one starts a daemon that ANY `plug <command>` already starts:
	// the daemon is machine-wide by design, so refusing the verb here would
	// change nothing an attacker could not get by running plug normally. What
	// bounds the damage is not who may start the daemon, it is what a flow is
	// allowed to reach once it is up, which is the ownership check in
	// internal/tun/router.go.
	if len(os.Args) > 1 && os.Args[1] == tun.DaemonVerb {
		os.Exit(daemonMain(os.Args[2:]))
	}
	// Core mode: this binary was exec'd by the launcher to do the real work.
	if os.Getenv("PLUG_CORE") == "1" {
		coreMain()
		return
	}

	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage())
		os.Exit(2)
	}
	// A verb this build does not carry is refused here, before the switch. It
	// must not reach launcherRun either: that would take `update` for the name of
	// a program to run inside the cluster and fail on something unrelated.
	if why, ok := verbAvailable(args[0]); !ok {
		refuseVerb(args[0], why)
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage())
		return
	case "version":
		cmdVersion(args[1:])
		return
	case "about":
		cmdAbout()
		return
	case "versions":
		listVersions()
		return
	case "prune":
		cmdPrune()
		return
	case "init":
		initProfile()
		return
	case "ls":
		cmdListProfiles()
		return
	case "rm":
		cmdRemoveProfile(args[1:])
		return
	case "rn", "mv":
		cmdRenameProfile(args[1:])
		return
	case "test":
		cmdTestProfile(args[1:])
		return
	case "doctor":
		cmdDoctor(args[1:])
		return
	case "mounts":
		cmdMounts()
		return
	case "mcp":
		cmdMCP(args[1:])
		return
	case "update":
		cmdUpdate(args[1:])
		return
	case "config":
		cmdConfig(args[1:])
		return
	case "keygen":
		cmdKeygen(args[1:])
		return
	case "pubkey":
		cmdPubkey(args[1:])
		return
	case "uninstall":
		uninstall(args[1:])
		return
	case "down":
		cmdDown(args[1:])
		return
	case "install-service":
		installService() // Windows: create the SCM datapath service (elevated, once)
		return
	case "remove-service":
		removeService()
		return
	case "selftest":
		os.Exit(runSelfTest())
	}
	launcherRun(args)
}

// ---- core (the real tunnel work; runs when PLUG_CORE=1 or in-process) ----

// coreEnv builds the environment for the privileged core re-exec. Host/port/forwards
// travel over PLUG_CORE_* vars — a private launcher→core channel, NOT a user-facing
// option (there is none: the cluster comes from --host or a profile).
//
// COMPAT: launcher and core are routinely DIFFERENT versions (an installed setuid
// launcher execs the cluster's exact core, older or newer — that's the launcher
// model). The env channel is therefore an inter-version protocol: also set the
// legacy PLUG_HOST/PLUG_PORT names so an older downloaded core still finds its
// cluster, and coreMain reads the legacy names as a fallback for older launchers.
func coreEnv(cfg config) []string {
	// The core is a SECOND privileged process: it narrows its own $PATH on entry
	// and resolves YOUR command against the one it inherits from here. But
	// os.Environ() already carries the PATH securePath narrowed in THIS process,
	// so handing it over straight would make the core take the narrowed one for
	// the human's — and never find a node/npm that lives in nvm or Homebrew.
	env := append(withUserPath(os.Environ()), "PLUG_CORE=1",
		"PLUG_CORE_HOST="+cfg.host, "PLUG_CORE_PORT="+cfg.port,
		// The profile's private key. The core is the process that opens the
		// TUNNEL, so a key that stops here is a key never offered: the agent saw
		// only the built-in one and refused an enrolled developer by a
		// fingerprint they had never seen. It travels with the host and the port
		// for the same reason the update policy does - the core is given a
		// cluster, not a profile name.
		"PLUG_CORE_KEY="+cfg.key,
		// The core runs the background check but never knows which profile it
		// came from — it is given a host and a port, not a name. So the policy
		// travels with them.
		"PLUG_CORE_UPDATE="+cfg.updateMode,
		"PLUG_HOST="+cfg.host, "PLUG_PORT="+cfg.port) // legacy channel for older cores
	return env
}

// coreGetenv reads a launcher→core variable: the current name first, then the
// legacy one (an OLDER launcher exec'ing this newer core only sets the legacy names).
func coreGetenv(name, legacy string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return os.Getenv(legacy)
}

// coreConfigFromEnv rebuilds the cluster the launcher chose. It is the ONLY
// channel between the two processes, so a field missing here is a field the core
// does not have - and the core is the process that opens the tunnel, runs the
// datapath and talks to the agent.
//
// That is not hypothetical: the profile's key was absent from this struct, so
// `plug keygen` wrote a key, `plug pubkey` printed it, the developer enrolled it,
// and the tunnel offered the built-in key alone. The agent refused a fingerprint
// nobody recognised. Extracted into a function so the round-trip with coreEnv can
// be tested rather than trusted.
func coreConfigFromEnv() config {
	cfg := config{
		host:       coreGetenv("PLUG_CORE_HOST", "PLUG_HOST"),
		port:       coreGetenv("PLUG_CORE_PORT", "PLUG_PORT"),
		updateMode: os.Getenv("PLUG_CORE_UPDATE"),
		key:        os.Getenv("PLUG_CORE_KEY"),
	}
	if cfg.port == "" {
		cfg.port = defaultPort
	}
	return cfg
}

// runCore executes the tunnel logic in this very process — the single point
// where a process becomes the core, whether it got there by matching the
// cluster's version, by falling back to itself, or by being exec'd as the
// downloaded core (coreMain). Named local ports are resolved HERE for that
// reason: it is the last stop before the mappings are armed and the child is
// spawned, and the child inherits the environment this call sets.
func runCore(cfg config, cmdArgs []string) {
	specs, args, err := resolvePortVars(cfg.exposes, cmdArgs)
	if err != nil {
		fatal("%v", err)
	}
	cfg.exposes = specs
	// The core is the only side that lives long enough to ask: the launcher has
	// exec'd into this process and is gone. Detached on purpose — the session
	// never waits on it, and what it learns is for the NEXT launch.
	go backgroundUpdateCheck(cfg)
	os.Exit(coreRun(cfg, args))
}

func coreMain() {
	cfg := coreConfigFromEnv()
	lead, cmdArgs, err := stripLeadingAll(os.Args[1:]) // -c strips to an empty exposes list: exactly the pure-outbound datapath
	if err != nil {
		fatal("%v", err)
	}
	cfg.exposes = lead.specs
	cfg.envPolicy = lead.policy
	attachMounts(&cfg, lead.mounts)
	attachMountPolicy(&cfg, lead.noMount, lead.noMountList)
	if len(cmdArgs) == 0 {
		fatal("core: no command")
	}
	runCore(cfg, cmdArgs)
}

func info(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "[plug] "+format+"\n", a...)
}

func fatal(format string, a ...any) {
	info(format, a...)
	os.Exit(1)
}
