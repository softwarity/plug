package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// usage lists the everyday commands only — no implementation talk, and no rarely
// needed ones (down: the background tears itself down — still works, just unlisted).
func usage() string {
	return `plug — run a local command as a member of your cluster.

Usage:
  plug [-p profile] -s <name>:<cluster-port>:<local-port> <command> [args...]
                                       run <command> as a named member of the
                                       cluster — it answers to <name>, and
                                       reaches cluster services by name in return
  plug [-p profile] -c <command> [args...]
                                       run <command> as a pure client of the
                                       cluster — reaches services by name, is
                                       never called back (DB tools, scripts)
  plug [-p profile] {-s …|-c} --dockerrun docker run <image> [args...]
                                       same, but the member is a CONTAINER: your
                                       image joins the cluster unmodified, and
                                       still names itself with -s or declares
                                       itself a client with -c
  plug ls                              list profiles
  plug test [profile]                  check an agent is reachable
  plug doctor [-p profile] [--fix]     health-check everything plug touches
  plug mounts                          list the volumes live sessions have mounted here
  plug mcp                     an MCP server over stdio, for an AI coding agent: the
                               cluster's names, doctor, a workload's environment, as tools
                                       (binaries, resolver, service, clusters),
                                       apply the safe repairs with --fix, and
                                       offer to report problems as an issue
` + usageFor("update") + usageFor("keys") + `  plug rn <old> <new>                  rename a profile (alias: mv)
  plug rm <profile>                    remove a profile
  plug version [-p profile]            this launcher's version — or, with a
                                       profile, that cluster agent's version
` + usageFor("versions") + `  plug prune                           delete the cached cores no cluster runs
                                       any more (asks each profile's agent)
  plug uninstall                       remove plug from this machine
  plug about                           what plug is, in a few lines

Options:
  -p, --profile <name>   use profile ~/.plug/<name>.conf
  -H, --host <host>      agent host
      --port <port>      agent SSH port (default 2222)
  -s, --serve <name>:<cluster-port>:<local-port>
                         publish this process in the cluster as <name>: workloads
                         reaching <name>:<cluster-port> land on 127.0.0.1:<local-port>
                         for this session. The agent creates the name on the fly
                         (Docker socket / Kubernetes RBAC), or you pre-declare it.
                         If a deployed workload already owns the name, it is
                         parked for the session (containers stopped, Swarm
                         service scaled to 0, k8s Service repointed) and
                         restored when the session ends.
                         Repeatable, and the options may come in any order.
                         <local-port> may be a NAME instead of a number — plug
                         then picks a free port for the session and writes it
                         into your command wherever {NAME} appears. Nothing to
                         pin, nothing to collide:
                           plug -s web:8080:PORT npm run dev -- --port={PORT}
  -c, --client           this process only CONSUMES the cluster — nothing to
                         name, no port reserved on the agent. For GUI DB tools
                         (DBeaver, Compass…), one-off scripts, batch consumers.
                         Mutually exclusive with -s.
      --no-env [A,B]     do NOT give the command the environment of the workload
                         it replaces. By default a -s that takes over a deployed
                         service hands its variables to your command, secrets
                         included, your own variables winning; --no-env alone
                         turns that off, --no-env A,B leaves out those keys.
      --env-of <name>    give the command the environment of THAT deployed
                         workload instead: a -c one-off script run with a
                         service's own credentials, or a -s that borrows the
                         variables of the service it talks to rather than of
                         the one it replaces. Same rules: secrets included,
                         yours winning, --no-env A,B still leaving keys out.
      --mount <spec>     mount one of the workload's VOLUMES here, live, read-write,
                         for the session - a Docker volume, a bind, a PVC - through
                         the SMB client your OS already has (nothing to install):
                           --mount /data                its /data, at /data here
                           --mount data:/srv/data       its volume "data", at /srv/data
                           --mount api:data:/srv/data   the volume "data" of "api"
                         Unnamed, the workload is the -s one or the --env-of one.
                         Repeatable. On Windows: a drive letter (--mount /data:Y:).
                         BY DEFAULT every data volume of the workload a -s takes
                         over (or --env-of names) is mounted without being told:
                         under the session's temp dir, at its cluster path, the
                         variables naming it repointed - the process finds its
                         data where its environment says. --mount is the explicit
                         form, at the exact path, for a process that hard-codes one.
      --no-mount [/a,/b] do NOT mount the workload's volumes; --no-mount /a,/b
                         leaves out those cluster paths and mounts the rest.
      --dockerrun        put a CONTAINER in the cluster, not a process:
                           plug -p prod -c --dockerrun docker run my-image
                         Prefixing docker with plug alone cannot work: the
                         container is created by the docker daemon, not as a
                         child of plug, so nothing of it goes through the
                         tunnel. This holds the datapath in a sidecar container
                         and runs yours in its network namespace, unmodified.
                         docker run only, and foreground only: a detached
                         container would outlive the network it was given.
  -h, --help             show this help

  Options may be written in ANY ORDER, and a long one takes its value either way
  (--profile prod or --profile=prod). They all come BEFORE your command; from
  your command on, every word is its own, flags included. Use -- if your own
  command's name starts with a dash.
`
}

// cmdAbout explains the concept in a few lines — the "why", not the plumbing.
func cmdAbout() {
	fmt.Print(`plug runs your local command as a full member of your cluster, in both
directions: cluster service names resolve, and your process is itself reachable
in the cluster under a name — no code change, no proxy config.

Set it up once per cluster (the install grants the privilege plug needs), then:

  plug -s my-app:8080:3000 npm run start:dev

Your process now reaches cluster services by name and answers at my-app:8080.
If a deployed service already owns that name, it is parked for the session and
restored when you stop.
Only consuming the cluster (a DB tool, a one-off script)? plug -c <command>.
Several clusters? Just name one with -p — plug creates the profile on first run:

  plug -p staging -s my-app:8080:3000 npm run start:dev

Docs: ` + docURL(docHome) + `
`)
}

type options struct {
	profile string
	host    string
	port    string
	exposes []string // raw -s values; validated once, re-prefixed on the core exec
	client  bool     // -c/--client: pure consumer — no name, nothing served
	// dockerRun: put a CONTAINER in the cluster instead of a process. A different
	// mode rather than a variation, and explicit rather than detected: see
	// dockerrun.go for why prefixing docker with plug cannot work, and why
	// rewriting somebody's `docker run` without being asked would be worse.
	dockerRun bool
	// noEnv: --no-env was given; noEnvList is what followed it ("" = all).
	noEnv     bool
	noEnvList string
	// envOf: the --env-of name, the workload whose environment the command
	// gets instead of the parked one's.
	envOf string
	// mounts: raw --mount values; validated once, re-prefixed on the core exec
	// like -s (mount.go has the grammar). noMount: --no-mount was given;
	// noMountList is what followed it ("" = all).
	mounts      []string
	noMount     bool
	noMountList string
}

// policy is the environment policy the three flags add up to. The one
// contradiction they can express is refused here, before anything connects.
func (o options) policy() envPolicy {
	p, err := envPolicyOf(o.noEnv, o.noEnvList, o.envOf)
	if err != nil {
		fatal("%v", err)
	}
	return p
}

// parseArgs reads plug's own options and hands back everything from the first
// thing that is not one. The options may come in ANY ORDER; only the command has
// to be last, and once it starts, every remaining word is its own.
//
// Three shapes are accepted for the same reason: people type them.
//
//   - `--profile prod` and `--profile=prod`. The second used to fall through to
//     the command, so `plug --profile=prod -c psql` complained about a missing
//     -s/-c: the real fault was the equals sign, and the message pointed
//     somewhere else entirely.
//   - `--` ends plug's options, for a command whose own name starts with a dash.
//
// An unknown flag is NOT refused, and that is deliberate rather than lazy. It
// stops the parse and goes to the command untouched, which is how a launcher
// carries a flag it has never heard of to a core that has: a launcher is
// installed once and runs cached cores for months, so refusing what it does not
// recognise would freeze the flag surface at whatever it knew on install day.
// TestParseArgsServe states that contract; refusing typos would have broken it,
// and the cost of keeping it is that `plug -c --typo prog` tries to run --typo.
func parseArgs(args []string) (options, []string) {
	var o options
	i := 0
	for i < len(args) {
		arg := args[i]
		if arg == "--" { // everything after this belongs to the command
			return o, args[i+1:]
		}
		// A long option may carry its value after an equals sign. Split it here
		// so the switch below sees the same name either way.
		name, inline, glued := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.IndexByte(arg, '='); eq > 0 {
				name, inline, glued = arg[:eq], arg[eq+1:], true
			}
		}
		value := func() string {
			if glued {
				if inline == "" {
					fatal("missing value for %s", name)
				}
				return inline
			}
			return flagValue(args, &i)
		}
		// A value glued to an option that takes none is not ours to judge: some
		// future core may well define it. Fall through and let it travel, on the
		// same reasoning as an unknown flag.
		if glued && !valueOptions[name] {
			return o, args[i:]
		}
		switch name {
		case "-h", "--help":
			fmt.Print(usage())
			os.Exit(0)
		case "-p", "--profile":
			o.profile = value()
		case "-H", "--host":
			o.host = value()
		case "--port":
			o.port = value()
		case "-s", "--serve":
			o.exposes = append(o.exposes, value())
		case "-c", "--client":
			o.client = true
		case "--dockerrun":
			o.dockerRun = true
		case "--no-env":
			o.noEnv = true
			// Optional value: `--no-env` alone, `--no-env A,B`, or `--no-env=A,B`.
			// The glued form used to fall through as an unknown flag (it was not
			// a valueOption) and travel to the command, which then failed on an
			// argument it had never asked for.
			if glued {
				o.noEnvList = inline
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && strings.Contains(args[i+1], "=") == false && looksLikeKeyList(args[i+1]) {
				o.noEnvList = args[i+1]
				i++
			}
		case "--env-of":
			o.envOf = value()
		case "--mount":
			o.mounts = append(o.mounts, value())
		case "--no-mount":
			o.noMount = true
			if glued {
				o.noMountList = inline
			} else if i+1 < len(args) && looksLikePathList(args[i+1]) {
				o.noMountList = args[i+1]
				i++
			}
		default:
			return o, args[i:]
		}
		i++
	}
	return o, nil
}

// valueOptions are the long options that take one, which is all the equals form
// has to know: everything else keeps travelling as it was written.
var valueOptions = map[string]bool{
	"--profile": true, "--host": true, "--port": true, "--serve": true, "--env-of": true, "--mount": true,
	"--no-env":   true, // optional value; the case above reads the glued form itself
	"--no-mount": true,
}

func flagValue(args []string, i *int) string {
	if *i+1 >= len(args) {
		fatal("missing value for %s", args[*i])
	}
	*i++
	return args[*i]
}

// serveRequired enforces the invocation shape: a command either joins the
// cluster AS a named member (-s — a running process in a cluster is a service,
// and a service has a name) or declares itself a PURE CLIENT (-c — a GUI DB
// tool, a one-off script: nothing will ever call it, so there is nothing to
// name and no port to reserve on the agent). One or the other, never both,
// never neither. Run before connecting, so a wrong shape fails instantly.
// Subcommands never reach here: main() dispatches ls/test/about/… first.
func serveRequired(exposes []string, client bool) error {
	if client && len(exposes) > 0 {
		return errors.New("-s and -c are mutually exclusive: a process either serves a name in the cluster or is a pure client")
	}
	if !client && len(exposes) == 0 {
		return errors.New("tell plug what this process is to the cluster:\n" +
			"  plug [-p profile] -s <name>:<cluster-port>:<local-port> <command> [args...]   # a service: the cluster can call it by name\n" +
			"  plug [-p profile] -c <command> [args...]                                      # a pure client: DB tools, one-off scripts\n" +
			"-s: a running process in a cluster is a service, and a service has a name — name it,\n" +
			"    even when nothing calls it back yet.\n" +
			"-c: this process only consumes the cluster — nothing to name, no port reserved on the agent.")
	}
	for _, r := range exposes {
		if _, err := parseExpose(r); err != nil {
			return err
		}
	}
	return nil
}

// hasServeFlag reports whether a -s / --serve token appears among the command's
// args — used to give a precise hint when -s was placed after the command.
func hasServeFlag(args []string) bool {
	for _, a := range args {
		if a == "-s" || a == "--serve" || strings.HasPrefix(a, "--serve=") ||
			a == "-c" || a == "--client" {
			return true
		}
	}
	return false
}

// exposeName is the RFC 1035 label both agent backends accept (leading letter,
// so the name is a valid Kubernetes Service too). Mirrored here so a bad -s name
// fails instantly, before connecting, with the same rule the agent enforces.
var exposeName = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// parseExpose parses one -s value, <name>:<cluster-port>:<local-port> — the
// reverse direction (see tunnel/expose.go).
func parseExpose(s string) (tunnel.ExposeSpec, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 || parts[0] == "" {
		return tunnel.ExposeSpec{}, fmt.Errorf("-s wants <name>:<cluster-port>:<local-port>, got %q", s)
	}
	if !exposeName.MatchString(parts[0]) {
		return tunnel.ExposeSpec{}, fmt.Errorf("-s %s: %q is not a valid name — a cluster DNS name is a "+
			"lowercase letter then letters, digits or hyphens (max 63), e.g. my-app", s, parts[0])
	}
	// The cluster port is what other workloads dial — it is agreed in advance,
	// so it is always a number.
	if !validPort(parts[1]) {
		return tunnel.ExposeSpec{}, fmt.Errorf("-s %s: %q is not a valid port", s, parts[1])
	}
	// The LOCAL port may instead be NAMED, and plug allocates a free one per
	// session (see portvar.go).
	if validPort(parts[2]) {
		return tunnel.ExposeSpec{Name: parts[0], ClusterPort: parts[1], LocalPort: parts[2]}, nil
	}
	if !portVarName.MatchString(parts[2]) {
		return tunnel.ExposeSpec{}, fmt.Errorf("-s %s: %q is neither a port nor a variable name — "+
			"give a number, or name it to have plug pick a free one (-s %s:%s:PORT, then {PORT} "+
			"in your command)", s, parts[2], parts[0], parts[1])
	}
	return tunnel.ExposeSpec{Name: parts[0], ClusterPort: parts[1], PortVar: parts[2]}, nil
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// attachExposes parses the raw -s values for an in-process core run — the
// grammar here IS this binary's grammar, so failing now is legitimate. (The
// exec path forwards them raw instead: the downloaded core owns the grammar.)
func attachExposes(cfg *config, raw []string) {
	for _, r := range raw {
		spec, err := parseExpose(r)
		if err != nil {
			fatal("%v", err)
		}
		cfg.exposes = append(cfg.exposes, spec)
	}
}

// attachMounts parses the raw --mount values and resolves the workload each
// one belongs to against what is already known: the -s names and --env-of.
// After attachExposes and the policy, since both are what "implied" means.
func attachMounts(cfg *config, raw []string) {
	if len(raw) == 0 {
		return
	}
	var specs []mountSpec
	for _, r := range raw {
		spec, err := parseMount(r)
		if err != nil {
			fatal("%v", err)
		}
		specs = append(specs, spec)
	}
	specs, err := resolveMountNames(specs, cfg.exposes, cfg.envPolicy.from)
	if err != nil {
		fatal("%v", err)
	}
	cfg.mounts = specs
}

// attachMountPolicy is --no-mount's half of the same.
func attachMountPolicy(cfg *config, noMount bool, list string) {
	if noMount {
		cfg.mountPolicy = parseNoMount(list)
	}
}

// leadingFlags is everything a launcher puts at the head of the core's argv.
type leadingFlags struct {
	specs       []tunnel.ExposeSpec
	client      bool
	policy      envPolicy
	mounts      []string // raw --mount values, resolved by attachMounts once exposes are known
	noMount     bool
	noMountList string
}

// stripLeadingAll pops what a launcher left at the head of the core's argv
// (see encodeCoreArgv): the -s/--serve pairs, -c, --no-env, --env-of and the
// mount flags. An old launcher forwards them there without understanding
// them, and the core, which owns the grammar, strips them back.
func stripLeadingAll(args []string) (leadingFlags, []string, error) {
	var lead leadingFlags
	var specs []tunnel.ExposeSpec
	client := false
	policy := envPolicy{}
	for {
		switch {
		case len(args) >= 2 && args[0] == "--mount":
			lead.mounts = append(lead.mounts, args[1])
			args = args[2:]
		case len(args) >= 1 && args[0] == "--no-mount":
			lead.noMount = true
			args = args[1:]
			if len(args) >= 1 && looksLikePathList(args[0]) {
				lead.noMountList = args[0]
				args = args[1:]
			}
		case len(args) >= 1 && args[0] == "--no-env":
			args = args[1:]
			from := policy.from // --env-of may have come first; keep it
			if len(args) >= 1 && looksLikeKeyList(args[0]) {
				policy = parseNoEnv(args[0])
				args = args[1:]
			} else {
				policy = parseNoEnv("")
			}
			policy.from = from
		case len(args) >= 2 && args[0] == "--env-of":
			policy.from = args[1]
			args = args[2:]
		case len(args) >= 2 && (args[0] == "-s" || args[0] == "--serve"):
			spec, err := parseExpose(args[1])
			if err != nil {
				return leadingFlags{}, nil, err
			}
			specs = append(specs, spec)
			args = args[2:]
		case len(args) >= 1 && (args[0] == "-c" || args[0] == "--client"):
			client = true
			args = args[1:]
		default:
			if policy.off && policy.from != "" {
				// The launcher refuses this before connecting; the core says it
				// too, for an argv that reached it from an older launcher.
				_, err := envPolicyOf(true, "", policy.from)
				return leadingFlags{}, nil, err
			}
			lead.specs, lead.client, lead.policy = specs, client, policy
			return lead, args, nil
		}
	}
}

// looksLikeKeyList tells "A,B" (a --no-env value) from the command that follows
// the flag: variable names, commas, nothing else. A command like `npm` is one
// word with no comma and no upper case, and is left where it is.
func looksLikeKeyList(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == ',':
		default:
			return false
		}
	}
	return true
}
