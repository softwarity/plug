package main

import (
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

// ---- launcher ----

// launcherRun resolves the cluster, learns its version, and executes the
// matching core binary (downloading it once if needed).
func launcherRun(args []string) {
	opts, cmdArgs := parseArgs(args)
	// `plug -p X update` reads as naturally as `plug update -p X` — accept both
	// (same for doctor and version). Re-route the pre-parsed flags to the
	// subcommand.
	if redispatchSubcommand(opts, cmdArgs) {
		return
	}
	if len(cmdArgs) == 0 {
		// `plug -p <name>` with no command creates (or reconfigures) that profile.
		// With -H/--port too it's written non-interactively (scriptable); otherwise
		// the wizard asks. Bare `plug` with no -p still just shows usage.
		if len(opts.exposes) > 0 {
			fatal("-s serves a local port for the lifetime of a session — give plug a command to run")
		}
		if opts.client {
			fatal("-c runs a command as a pure client of the cluster — give plug a command to run")
		}
		if opts.profile != "" {
			name := opts.profile
			if opts.host != "" {
				port := opts.port
				if port == "" {
					port = defaultPort
				}
				writeProfile(name, opts.host, port)
			} else {
				name = wizard(name, true)
			}
			info("try it:  plug -p %s <your command>", name)
			return
		}
		fatal("no command given\n\n" + usage())
	}
	if err := serveRequired(opts.exposes, opts.client); err != nil {
		hint := ""
		if hasServeFlag(cmdArgs) {
			// A -s after the command word was passed TO the command (plug stops
			// parsing its own flags at the first operand) — the likely mistake.
			hint = "\n\nnote: a -s/-c AFTER the command goes to the command; plug's flags must come BEFORE it:\n" +
				"  plug -s <name>:<cluster-port>:<local-port> " + strings.Join(cmdArgs, " ") + "\n" +
				"  plug -c " + strings.Join(cmdArgs, " ")
		}
		fatal("%s%s\n\n%s", err, hint, usage())
	}
	cfg := resolveConfig(opts)
	if cfg.host == "" {
		fatal("no agent host: use --host or a profile in ~/.plug/")
	}

	// --dockerrun leaves before everything below. It starts no datapath on this
	// machine and runs no child here: the tunnel lives in a container, and the
	// version dance below is about the core THIS host would have run. The env
	// policy is resolved here too, since this host is where the projection into
	// the container is built (the sidecar only holds the datapath).
	if opts.dockerRun {
		cfg.dockerRun = true
		cfg = finalizeInProcess(cfg, opts)
		os.Exit(runDockerRun(cfg, cmdArgs, opts.exposes, opts.client))
	}

	remote, err := agentVersion(cfg)
	if err != nil {
		fatal("cannot reach the agent at %s:%s: %v", cfg.host, cfg.port, err)
	}

	env := coreEnv(cfg)

	// Same version as this launcher (or the agent is unversioned): run in-process.
	if remote == version || remote == "" {
		runCore(finalizeInProcess(cfg, opts), cmdArgs)
		return
	}
	checkAgentCompat(remote, opts)
	// A core older than the personal key would drop it: the key travels to the
	// core in PLUG_CORE_KEY and a core that does not read that variable opens the
	// tunnel with the built-in key alone. The agent then refuses a fingerprint
	// the developer has never seen, while `plug test` - which never leaves this
	// process - authenticates perfectly. Two answers from one machine, and the
	// working one is the one that proves nothing.
	//
	// The version that runs is the CLUSTER's, so an agent published before the
	// feature disables it for every client of that cluster, however new. Run our
	// own core instead: it is the same fallback taken when the download fails,
	// and losing the version match costs less than losing the identity.
	if coreDropsProfileKey(remote, cfg.key) {
		info("this cluster runs v%s, whose core predates per-profile keys and would offer only the\n"+
			"      shared key built into plug. Running this launcher's core (v%s) instead, so the\n"+
			"      identity in your profile is the one presented. Upgrade the agent to line them up.",
			shortVersion(remote), shortVersion(version))
		runCore(finalizeInProcess(cfg, opts), cmdArgs)
		return
	}
	announceUpdate(cfg) // what a previous session found, said before the core takes over
	core, err := ensureVersion(remote, cfg)
	if err != nil {
		info("cannot fetch v%s (%v) — falling back to this launcher (v%s)", remote, err, version)
		runCore(finalizeInProcess(cfg, opts), cmdArgs)
		return
	}
	info("using cluster version v%s", shortVersion(remote))
	// Linux: file capabilities do not survive exec'ing the downloaded core, so
	// they are re-raised as AMBIENT ones, which do. Both halves of that —
	// capset() and PR_CAP_AMBIENT_RAISE — apply to the calling THREAD, not the
	// process, while Go moves goroutines between OS threads at any scheduling
	// point. Unpinned, the fork in execCore can leave from a thread that never got
	// them, and the core starts unprivileged: "plug needs the privileged setup",
	// on a machine where the install did grant it.
	//
	// So pin the goroutine to the thread being granted, and keep it pinned
	// through the fork — os/exec clones from the calling thread. Never unlocked:
	// this goroutine's remaining job is to wait for the child.
	//
	// It has always been a race; it became a likely one when the SIGINT handling
	// in execCore added a Notify and a goroutine start between the raise and the fork.
	// It cost a red CI leg on Linux only, intermittently — the shape of a
	// per-thread state leak.
	runtime.LockOSThread()
	raiseAmbientCaps()
	execCore(core, encodeCoreArgv(opts, cmdArgs), env, remote)
}

// redispatchSubcommand runs the subcommand a flag-first spelling put after
// plug's options (`plug -p X update`, `plug -p X doctor`, `plug -p X version`)
// and reports whether it did. Any other command word is the person's own
// program, and launcherRun runs it.
func redispatchSubcommand(opts options, cmdArgs []string) bool {
	if len(cmdArgs) == 0 || (cmdArgs[0] != "update" && cmdArgs[0] != "doctor" && cmdArgs[0] != "version") {
		return false
	}
	// The same refusal as the top-level dispatch. `plug -p X update` gets here
	// with "-p" as args[0], so the guard up there never saw the verb: without
	// this, the one spelling refuses and the other runs.
	if why, ok := verbAvailable(cmdArgs[0]); !ok {
		refuseVerb(cmdArgs[0], why)
	}
	if len(opts.exposes) > 0 || opts.client {
		fatal("-s/-c don't apply to %q", cmdArgs[0])
	}
	sub, rest := cmdArgs[0], cmdArgs[1:]
	if sub != "doctor" { // doctor has no host flags — profiles only
		if opts.port != "" {
			rest = append([]string{"--port", opts.port}, rest...)
		}
		if opts.host != "" {
			rest = append([]string{"-H", opts.host}, rest...)
		}
	}
	if opts.profile != "" {
		rest = append([]string{"-p", opts.profile}, rest...)
	}
	switch sub {
	case "update":
		cmdUpdate(rest)
	case "doctor":
		cmdDoctor(rest)
	default:
		cmdVersion(rest)
	}
	return true
}

// finalizeInProcess attaches the parsed flags to cfg for a core that runs in
// THIS process, whether by matching the cluster's version, by falling back to
// this launcher, or because --dockerrun builds the projection here: the -s
// values, the environment policy, the --mount values and the --no-mount
// policy. attachMounts comes after the first two, which it resolves against.
func finalizeInProcess(cfg config, opts options) config {
	attachExposes(&cfg, opts.exposes)
	cfg.envPolicy = opts.policy()
	attachMounts(&cfg, opts.mounts)
	attachMountPolicy(&cfg, opts.noMount, opts.noMountList)
	return cfg
}

// checkAgentCompat refuses, before anything is downloaded, the flags a core of
// the agent's version would not understand. Each one is a feature with a first
// release, and an older core would exec the flag as the command: say which
// feature it is and what to do, instead of an opaque exit 127.
func checkAgentCompat(remote string, opts options) {
	// -s is a 2.0.0 feature. A released agent from before it (major < 2) dictates
	// a core that would exec "-s" as the command: an opaque exit 127. Refuse with
	// the remedy instead. (dev/unversioned agents do not parse and are assumed new.)
	if len(opts.exposes) > 0 && versionBefore(remote, 2, 0) {
		fatal("the cluster agent reports v%s, which predates -s (needs plug ≥ 2.0.0).\n"+
			"Upgrade the agent (redeploy the softwarity/plug image), then run again.", remote)
	}
	// -c is a 2.2 feature — an older released core would exec "-c" as the command.
	if opts.client && versionBefore(remote, 2, 2) {
		fatal("the cluster agent reports v%s, which predates -c (needs plug ≥ 2.2).\n"+
			"Upgrade the agent (redeploy the softwarity/plug image), then run again.", remote)
	}
	// --mount is a 2.20 feature: the agent's mount-volume verb AND the core's
	// flag. An older core would exec "--mount" as the command.
	if len(opts.mounts) > 0 && versionBefore(remote, 2, 20) {
		fatal("the cluster agent reports v%s, which predates --mount (needs plug ≥ 2.20).\n"+
			"Upgrade the agent (redeploy the softwarity/plug image), then run again.", remote)
	}
	// A NAMED local port (-s web:8080:PORT) is a 2.4 feature. The mapping crosses
	// the exec raw, so an older core would parse that third field as a port and
	// reject it with a message about the port, not about the version. Say which
	// it is, and offer the pinned form that works on any agent.
	if versionBefore(remote, 2, 4) {
		for _, r := range opts.exposes {
			if spec, err := parseExpose(r); err == nil && spec.PortVar != "" {
				fatal("-s %s names its local port, which needs plug ≥ 2.4 — the cluster agent reports v%s.\n"+
					"Upgrade the agent (redeploy the softwarity/plug image), or pin the port for now "+
					"(-s %s:%s:<number>).", r, remote, spec.Name, spec.ClusterPort)
			}
		}
	}
}

// encodeCoreArgv serialises the flag families the launcher parsed back onto the
// head of the core's argv, in the wire format stripLeadingAll reads: this is
// that parser's symmetric half, and the two must agree on every flag.
func encodeCoreArgv(opts options, cmdArgs []string) []string {
	// -s mappings cross the exec RAW, as leading argv: the downloaded core owns
	// the grammar (validation included) — this launcher must not veto values a
	// newer core understands. coreMain strips them back. An old launcher doesn't
	// know -s and already forwards them in cmdArgs untouched — same wire format
	// both ways; an old core fails loudly on "-s" instead of silently not
	// exposing.
	for i := len(opts.exposes) - 1; i >= 0; i-- {
		cmdArgs = append([]string{"-s", opts.exposes[i]}, cmdArgs...)
	}
	if opts.client {
		cmdArgs = append([]string{"-c"}, cmdArgs...) // same wire format as -s: the core strips it back
	}
	opts.policy() // the contradiction is refused here, not by a core that may predate the flag
	if opts.noEnv {
		// Same wire format again. An old core fails loudly on an unknown flag
		// rather than silently projecting what the person asked it not to.
		if opts.noEnvList != "" {
			cmdArgs = append([]string{"--no-env", opts.noEnvList}, cmdArgs...)
		} else {
			cmdArgs = append([]string{"--no-env"}, cmdArgs...)
		}
	}
	if opts.envOf != "" {
		cmdArgs = append([]string{"--env-of", opts.envOf}, cmdArgs...)
	}
	// --mount crosses raw too, its grammar checked here first so a bad spec is
	// refused before anything connects, and stripped back by coreMain.
	for _, r := range opts.mounts {
		if _, err := parseMount(r); err != nil {
			fatal("%v", err)
		}
	}
	for i := len(opts.mounts) - 1; i >= 0; i-- {
		cmdArgs = append([]string{"--mount", opts.mounts[i]}, cmdArgs...)
	}
	if opts.noMount {
		if opts.noMountList != "" {
			cmdArgs = append([]string{"--no-mount", opts.noMountList}, cmdArgs...)
		} else {
			cmdArgs = append([]string{"--no-mount"}, cmdArgs...)
		}
	}
	return cmdArgs
}

// execCore runs the downloaded core, named through the descriptor ensureVersion
// verified, with this launcher's stdio and env, and stays alive to relay a
// targeted SIGTERM while the terminal's SIGINT reaches the core by itself. It
// returns once the core has exited cleanly; otherwise it exits with its status.
func execCore(core *os.File, argv, env []string, remote string) {
	// Named through the descriptor we verified, not through its path — see
	// execTarget. The descriptor stays open until the child is started; the child
	// inherits it as fd 3, which is what it is then executed from.
	target, extra := execTarget(core)
	defer core.Close()
	child := exec.Command(target, argv...)
	child.ExtraFiles = extra
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = env
	// The launcher is the process the SHELL waits on. A terminal Ctrl-C is
	// delivered to the whole foreground group — launcher, core, the user's dev
	// server — and with no handler here the launcher died on the spot, handing
	// the prompt back while the rest of the group was still shutting down. The
	// dev server then restored the raw-mode terminal it owns AFTER readline had
	// already configured it, and arrow keys echoed ^[[A until the next Enter.
	// The race is timing-dependent, which is why it comes and goes with
	// unrelated teardown changes (f1e988f removed one trigger, not the race).
	//
	// So: survive SIGINT and keep waiting — the prompt returns only once the
	// core, its teardown and the child's own exit are all done. Same contract as
	// runChildEnv one level down: the child got the kernel's copy, never relay
	// INT (a second one is "force quit NOW" to dev servers — the f1e988f bug);
	// a targeted SIGTERM at the launcher alone is not group-delivered, so that
	// one is passed on.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		for s := range sigs {
			// child.Process is nil until Run below reaches its own Start, and this
			// goroutine is already listening: a SIGTERM landing in that window
			// dereferenced nil and took the launcher down with a panic instead of
			// passing the signal on. Microseconds wide, and reachable by anything
			// that signals plug the moment it starts, which is what a shell doing
			// job control does.
			if s == syscall.SIGTERM && child.Process != nil {
				child.Process.Signal(s)
			}
		}
	}()
	if err := child.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fatal("running v%s: %v", remote, err)
	}
}

// The first version whose CORE reads PLUG_CORE_KEY. Below it the launcher must
// not hand off, or the profile's key stops at the exec. Bump this at release
// time if the feature ships under a different number.
const profileKeyCoreMajor, profileKeyCoreMinor = 2, 12

// coreDropsProfileKey reports whether running a core of version coreVer would
// silently discard this profile's key. Pure, so the table of versions is a test
// rather than a claim: an unparseable version (dev, a branch build) is assumed
// new, exactly as every other guard here assumes.
func coreDropsProfileKey(coreVer, profileKey string) bool {
	if profileKey == "" {
		return false // nothing to drop
	}
	return versionBefore(coreVer, profileKeyCoreMajor, profileKeyCoreMinor)
}
