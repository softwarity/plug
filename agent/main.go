// plug-agent is the agent-side helper for the reverse direction (-s): it
// provisions the CLUSTER NAME a session serves, dynamically, with whatever the
// deployment gives it — and nothing more:
//
//   - Docker/Swarm, docker.sock mounted (opt-in in the stack file): create a
//     tiny SIGNPOST container carrying the DNS alias, relaying the port to the
//     agent. Names appear and disappear with the session — no stack redeploy.
//   - Kubernetes (RBAC granted by deploy/plug-k8s.yaml): create a
//     Service selecting the agent pod. Same lifecycle.
//   - Neither: answer an error naming the access it is missing. There is no
//     fallback mode: a name you must pre-declare cluster-side is the very
//     coordination -s exists to remove.
//
// It is the ForceCommand of the `plug` SSH user, so it is ALSO the user's whole
// exec surface: the verbs below or nothing (no shell — a lockdown compared to
// the /bin/sh that user had before).
//
// Verbs (via SSH_ORIGINAL_COMMAND):
//
//	serve-name <name> <port> takeover
//	                           provision name:port → this agent. One line out:
//	                           "dynamic" | "dynamic parked" | "error: …"
//	                           A REAL workload owning the name is
//	                           parked for the session (containers stopped, Swarm
//	                           service scaled to 0, k8s Service repointed) — the
//	                           parking receipt rides the signpost's labels (or a
//	                           k8s annotation), and unserve-name/gc restore it.
//	unserve-name <name>        drop it, restoring anything parked
//	                           ("ok" | "error: …")
//
// Direct argv modes (not reachable over SSH):
//
//	preflight                  refuse to boot without orchestrator access
//
//	plug-agent signpost <port> <target>   the signpost container's process
//	plug-agent gc                         boot-time cleanup (entrypoint)
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Main is the standalone binary's whole body, kept here rather than in cmd/ so
// the argv dispatch and the verbs it reaches stay in one place. Meerkat does not
// call it: it calls Start (serve.go) for the server, and re-execs ITSELF with a
// hidden verb for the subprocess side, the way plug's own launcher does.
func Main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "signpost":
			// Pairs, because ONE container carries the DNS alias: a service
			// exposing several ports (HTTP+SMTP+POP3 on one name) still gets
			// one signpost, listening on all of them.
			if len(args) < 3 || len(args)%2 == 0 {
				fatal("usage: plug-agent signpost <port> <target> [<port> <target> …]")
			}
			signpost(args[1:])
			return
		case "gc":
			gc()
			return
		case "preflight":
			preflight()
			return
		case "serve":
			// The container's whole process: what entrypoint.sh and sshd did
			// between them, in one binary that never forks a privileged helper.
			serve(args[1:])
			return
		}
	}
	// ForceCommand path: the client's command line arrives in SSH_ORIGINAL_COMMAND.
	dispatch(strings.Fields(os.Getenv("SSH_ORIGINAL_COMMAND")))
}

// preflight refuses to start an agent that cannot do the job it is deployed
// for. plug exists to plug services into a cluster: an agent with no way to
// create a name can carry sessions but not serve one, and a deployment that
// only reveals that the first time someone runs -s has hidden a missing mount
// or a missing RBAC behind an otherwise healthy container. Fail here, once, at
// the moment the stack file is in front of whoever wrote it.
// preflight is the argv verb: refuses to boot, which is right for a dedicated
// agent (see Preflight for why an embedder must decide for itself).
func preflight() {
	if err := Preflight(); err != nil {
		fatal("%v", err)
	}
}

// Preflight reports whether this agent can provision cluster names at all.
//
// Returned rather than fatal, because the right reaction differs. A dedicated
// agent should die: a healthy-looking container that fails on someone's first
// -s hides a missing mount or a missing RBAC behind it. A gateway that embeds
// the agent must not: one forgotten rule would stop an otherwise working
// Meerkat from starting, for a feature its users may never touch.
func Preflight() error {
	if k8sAvailable() || dockerAvailable() {
		return nil
	}
	return errors.New("plug-agent: no orchestrator access, so this agent cannot create cluster names.\n" +
		"  Docker / Compose / Swarm: mount /var/run/docker.sock into the agent\n" +
		"      volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]\n" +
		"      (on Swarm, also run it as a service on a MANAGER node)\n" +
		"  Kubernetes: apply the RBAC that lets it manage Services\n" +
		"      kubectl apply -f deploy/plug-k8s.yaml\n" +
		"  Full stack files: " + docURL(docHome))
}

// A var, not a func, so a test can watch dispatch REFUSE something. Both exits
// leave the process, which made the validator that guards every command arriving
// from the network the one part of the agent no test could reach: nameRe could be
// widened to ^.*$ and the whole suite stayed green.
var fatal = func(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// answer prints the one-line protocol reply the CLI parses, and exits 0 — the
// reply itself carries success or failure ("error: …"), so the SSH exit status
// stays out of the contract (old CLIs never call us; old shells said 127).
var answer = func(format string, a ...any) {
	fmt.Printf(format+"\n", a...)
	os.Exit(0)
}

// A DNS label the way BOTH backends accept it: RFC 1035 (leading letter) so a
// k8s Service object is valid too — docker would take a leading digit, k8s
// won't, and -s must behave the same whichever backend answers.
var nameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// dispatch is the entry point for every command arriving over SSH. It validates
// and routes; the work of each verb lives in its own function below.
//
// It used to hold all seven bodies inline, at 60 cyclomatic complexity and 250
// lines, so reading what `resolve` does meant scrolling past everything `info`
// does. The split is mechanical and changes no logic: each case became a
// function with the same statements in the same order. What it buys is that the
// VALIDATION, which is what stands between the network and the cluster, is now
// visible in one screen instead of being interleaved with six implementations.
func dispatch(cmd []string) {
	if len(cmd) == 0 {
		fatal("plug-agent: this user runs the tunnel and the -s verbs; there is no shell")
	}
	switch cmd[0] {
	case "serve-name":
		doServeName(cmd)
	case "unserve-name":
		doUnserveName(cmd)
	case "info":
		doInfo(cmd)
	case "check-update":
		doCheckUpdate(cmd)
	case "resolve":
		doResolve(cmd)
	case "self-update":
		doSelfUpdate(cmd)
	default:
		answer("error: unknown command %q", cmd[0])
	}
}

func doServeName(cmd []string) {
	// One verb per NAME, all its ports at once — a service exposing
	// HTTP+SMTP+POP3 on one name is one signpost listening on all three.
	// Each pair is <cluster-port>:<agent-port>; the agent port is the
	// sshd-allocated port that session's forward listens on. Allocated, not
	// the cluster port itself: many names share one cluster port (every
	// service has its own IP in the cluster), but they all converge on ONE
	// agent, where a fixed port could bind only once.
	if len(cmd) != 4 || cmd[3] != "takeover" {
		answer("error: usage: serve-name <name> <cluster-port>:<agent-port>[,…] takeover")
	}
	name := cmd[1]
	if !nameRe.MatchString(name) {
		answer("error: %q is not a valid DNS label", name)
	}
	var pairs []portPair
	for _, pp := range strings.Split(cmd[2], ",") {
		c, a, ok := strings.Cut(pp, ":")
		if !ok {
			answer("error: %q is not <cluster-port>:<agent-port>", pp)
		}
		for _, p := range []string{c, a} {
			if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
				answer("error: %q is not a valid port", p)
			}
		}
		pairs = append(pairs, portPair{cluster: c, agent: a})
	}
	serveName(name, pairs)
}

func doUnserveName(cmd []string) {
	// The optional agent port says WHICH session is releasing the name, so a
	// session that lost it while it was away cannot tear down its successor's.
	if len(cmd) < 2 || len(cmd) > 3 || !nameRe.MatchString(cmd[1]) {
		answer("error: usage: unserve-name <name> [<agent-port>]")
	}
	mine := ""
	if len(cmd) == 3 {
		if n, err := strconv.Atoi(cmd[2]); err != nil || n < 1 || n > 65535 {
			answer("error: %q is not a valid port", cmd[2])
		}
		mine = cmd[2]
	}
	unserveName(cmd[1], mine)
}

func doInfo(cmd []string) {
	// One parsable line for `plug doctor`: the agent's version and which
	// dynamic -s backend THIS deployment actually has — the answer to "will
	// -s be dynamic here, and is the image current?" asked from the outside.
	ver := localVersion()
	backend := "none"
	switch {
	case k8sAvailable():
		backend = "kubernetes"
	case dockerAvailable():
		backend = "docker"
		var inf struct {
			Swarm struct {
				LocalNodeState string `json:"LocalNodeState"`
			} `json:"Swarm"`
		}
		if code, err := dockerAPI("GET", "/info", nil, &inf); err == nil && code == 200 &&
			inf.Swarm.LocalNodeState == "active" {
			backend = "docker-swarm"
		}
	}
	// The image the deployment carries, so the CLI can ask THAT registry
	// itself (plug update's client-side lookup) — best-effort: an agent
	// that cannot tell simply omits the field and the CLI falls back to
	// asking this agent to do the lookup.
	img := ""
	switch {
	case k8sAvailable():
		_, _, img, _, _ = k8sAgentDeployment()
	case dockerAvailable():
		if self, err := dockerSelf(); err == nil {
			img = self.image
		}
	}
	// Who the Host recognised behind this connection's key, passed in by the
	// server process (PLUG_WHO) because a verb runs in another process and
	// cannot ask the Host anything. Empty when the key names no person,
	// which is what the shared built-in key does and what a standalone
	// agent always answers. Omitted rather than sent empty: a field that is
	// there means somebody is identified.
	who := ""
	if w := strings.TrimSpace(os.Getenv("PLUG_WHO")); w != "" {
		who = " who=" + w
	}
	if img != "" {
		answer("version=%s backend=%s image=%s%s", ver, backend, img, who)
	}
	answer("version=%s backend=%s%s", ver, backend, who)
}

func doCheckUpdate(cmd []string) {
	// WHERE this deployment would go, without going there.
	//
	// The CLI's background check normally resolves this from the workstation,
	// against the registry the image lives in. That has no fallback by
	// design — "a timeout there is not a slower path, it is no check at all,
	// silently" — so a machine whose network cannot reach the registry is
	// never told anything. Measured on GitHub's macOS runners; it is also
	// the shape of a corporate network, a proxy, or a VPN that splits
	// routes, which is exactly where plug earns its keep.
	//
	// The cluster CAN reach the registry (it pulls from it), so ask it. One
	// line, same first-word contract as self-update, and no side effect:
	//   available <tag>   a newer release, or a moving tag that moved
	//   current           nothing to take
	//   error: …          could not tell (the CLI stays quiet, as before)
	//
	// An agent that predates this answers `unknown command`, which the CLI
	// already treats as "no answer" — so old clusters keep today's silence
	// rather than breaking.
	{
		img := ""
		switch {
		case k8sAvailable():
			_, _, img, _, _ = k8sAgentDeployment()
		case dockerAvailable():
			if self, err := dockerSelf(); err == nil {
				img = self.image
			}
		}
		if img == "" {
			answer("error: this agent cannot name its own image")
		}
		target, _, note := retarget(img)
		if target == "" || target == img || retargetImageOnly(target) == retargetImageOnly(img) {
			answer("current")
		}
		_, _, tag := parseImageRef(target)
		if note != "" {
			answer("available %s (%s)", tag, note)
		}
		answer("available %s", tag)
	}
}

func doResolve(cmd []string) {
	// Does <name> exist in THIS cluster? The CLI asks before minting a fake
	// IP for a bare name, so an absent name gets an honest NXDOMAIN instead
	// of a fake that can only refuse the connect (the Docker-Desktop
	// DNS-leak fix). Resolution runs here, through the cluster's own
	// resolver — the only place that truth lives. Both outcomes answer on
	// stdout ("found"/"nxdomain"): an error would be indistinguishable from
	// a pre-2.2 agent's "unknown command", which means "mint as before".
	if len(cmd) != 2 || !nameRe.MatchString(cmd[1]) {
		answer("error: usage: resolve <name>")
	}
	// The witness starts NOW, beside the lookup, not after it.
	//
	// It used to run only once the lookup had timed out, so an absent name on
	// a cluster whose resolver is a network hop away paid 800ms and THEN the
	// witness — two waits in series, of which only the second decides. That
	// is the same cascade this file already fixed once, one level down.
	//
	// It matters per family, and the measurement says so: four failures of
	// the honest-NXDOMAIN cell in ten runs, ALL of them on Kubernetes. The
	// witness there is kubernetes.default, answered by CoreDNS — a pod, a
	// network hop; on Docker and Swarm it is this agent's own container name,
	// answered by the daemon from memory in single-digit milliseconds. The
	// budgets were sized for the second and the first inherited them.
	//
	// Run side by side, the worst case is the LONGER of the two rather than
	// their sum, which is what pays for the witness's larger budget.
	witness := make(chan bool, 1)
	go func() { witness <- clusterResolverHealthy() }()

	ctx, cancel := context.WithTimeout(context.Background(), resolveLookupBudget)
	addrs, lerr := net.DefaultResolver.LookupHost(ctx, cmd[1])
	cancel()
	for _, a := range addrs {
		// 198.18.0.0/15 is the range plug itself mints fakes from — an
		// answer there can only be an ECHO of a plug resolver upstream
		// (cluster on a plugged workstation: embedded DNS → VM → host DNS
		// → plug), never a real cluster service. Filtering it here is what
		// makes the whole check immune to that loop.
		if ip := net.ParseIP(a).To4(); ip != nil && ip[0] == 198 && ip[1]&0xFE == 18 {
			continue
		}
		answer("found")
	}
	// Nothing came back — which is two different things. "This name does not
	// exist" is an answer; "I could not ask" is not, and answering nxdomain
	// to the second tells the CLI to serve a confident NXDOMAIN for a name
	// that may be perfectly alive, cached for 30s, machine-wide on macOS.
	//
	// The error type alone cannot separate them: in an isolated cluster
	// network an ABSENT name times out exactly like a dead resolver, because
	// the embedded DNS forwards it upstream and the upstream is unreachable.
	// A NXDOMAIN that arrives, though, is the resolver speaking — trust it.
	var dnsErr *net.DNSError
	if errors.As(lerr, &dnsErr) && dnsErr.IsNotFound {
		answer("nxdomain")
	}
	// Left with a timeout. Ask about something that MUST resolve here: if it
	// answers, the resolver is fine and the name really is absent; if it does
	// not, the resolver is the problem and we must not pass judgement on the
	// name. Anything the CLI does not recognise makes it fail open (mint as
	// before), so this needs no client change and old clients behave right.
	if lerr != nil && !<-witness {
		answer("unreachable")
	}
	answer("nxdomain")
}

func doSelfUpdate(cmd []string) {
	// An embedder can forbid it, and should: this verb rewrites the image of
	// the deployment the agent runs in. Standalone that deployment IS plug,
	// which is the point. Inside a gateway it is the gateway, and any
	// developer reaching the port could roll it onto a plug image. It finds
	// its target by the label app=plug, so a gateway not carrying that label
	// is already inert here, but "already inert" is not a decision anyone
	// made and a label is one line away from being copied.
	if os.Getenv(noSelfUpdateEnv) == "1" {
		answer("error: this agent does not update itself — it is embedded in another program, " +
			"which owns its own deployment and its own release cycle")
	}
	// An optional target names WHERE to go — a stream (latest, a branch) or
	// the word `tag` for the newest release. Without it, follow the tag the
	// deployment already carries.
	// `plug update` — move THIS agent to the newest release, each backend its
	// own way. A deployment pinned to a release tag has that tag REWRITTEN
	// (2.3.0 → 2.4.0): plug is infrastructure, not an application dependency
	// to hold back, and re-resolving a pin can only ever return the same
	// image. A moving tag (latest, main, a branch) belongs to its publisher
	// and is only re-pulled.
	//
	// One line out; the FIRST WORD is the verdict the CLI parses:
	//   updating …   a redeploy was triggered (k8s rolling / swarm update)
	//   current …    already the newest release, or a moving tag that did
	//                not move — answered WITHOUT rolling anything, so the
	//                CLI does not poll for a change that cannot come
	//   pulled …     newer image pulled; recreating is the caller's move
	//   error: …     no orchestrator access, RBAC gap, not a manager, …
	//   `apply <tag>` is the CLI-checked path: the caller already resolved
	//   the target against the registry this image lives in, so the agent
	//   applies it WITHOUT a lookup of its own — on a plugged workstation
	//   each registry round-trip from the cluster VM cost ~31s.
	if len(cmd) >= 2 && cmd[1] == "apply" {
		if len(cmd) != 3 || !tagRe.MatchString(cmd[2]) {
			answer("error: usage: self-update apply <tag>")
		}
		selfUpdate(applyPlan(cmd[2]))
	}
	want := ""
	if len(cmd) == 2 {
		want = cmd[1]
	} else if len(cmd) > 2 {
		answer("error: usage: self-update [<tag>|tag|latest]")
	}
	selfUpdate(followPlan(want))
}

// clusterResolverHealthy reports whether the cluster's DNS is answering at all,
// by asking it about something that MUST exist here. It is the only way to read
// a timeout: on its own, a timeout means either "absent, and the forward
// upstream is unreachable" or "the resolver is down", and those call for
// opposite answers.
//
// No witness available means no verdict, and no verdict means keep the previous
// behaviour (say nxdomain) rather than guess. That is deliberate: a wrong
// "healthy" would be harmless, but a wrong "unreachable" would mint fake
// addresses for names that really are absent — the DNS leak this whole check
// exists to close.
func clusterResolverHealthy() bool {
	w := resolverWitness()
	if w == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveWitnessBudget)
	defer cancel()
	addrs, _ := net.DefaultResolver.LookupHost(ctx, w)
	return len(addrs) > 0
}

// The CLI gives this agent cliResolveBudget to say whether a name exists, and
// MINTS a fake address if the answer is late — being slow is indistinguishable
// from being absent, from where it sits. Our own budgets must therefore fit
// INSIDE its, witness included.
//
// They did not: a 3s lookup followed by a 2s witness could only ever answer
// after the CLI had given up, so a slow lookup ALWAYS ended in a mint whatever
// this agent finally concluded. Two identical budgets in cascade.
//
// That is not a corner case. On Docker Desktop it is the normal path for an
// absent name: the embedded DNS forwards what it does not know upstream,
// upstream is the workstation, and the workstation is plugged — so the question
// comes back to the stub that asked it and nothing answers until something
// times out. Measured on a real machine: 3.03s, then a minted 198.18 address,
// on a perfectly current datapath.
//
// 800ms is generous for the only lookup that can legitimately succeed here: a
// cluster name is answered by the embedded resolver from memory, in single-digit
// milliseconds. Anything slower is not a cluster name being resolved — it is a
// question that has left the cluster.
const (
	resolveLookupBudget = 800 * time.Millisecond
	// 1.5s, and it costs nothing it did not already: the witness runs BESIDE the
	// lookup now, so the worst case is the longer of the two (1.5s) rather than
	// their sum — less than the 1.5s the old 800+700 cascade spent, with twice
	// the room for a resolver that answers over the network instead of from
	// memory. CoreDNS on a loaded kind node is exactly that resolver.
	resolveWitnessBudget = 1500 * time.Millisecond
	// cliResolveBudget mirrors the client's timeout (cli/internal/tunnel:
	// "a wedged session must not stall DNS"). Duplicated deliberately — the two
	// modules do not share code — and asserted in the tests, because the day it
	// drifts nothing fails: the CLI simply starts minting again, silently.
	cliResolveBudget = 3 * time.Second
)

// resolverWitness names something guaranteed to resolve in this cluster while
// its DNS works. Guaranteed by construction rather than by convention: every
// Kubernetes cluster has the kubernetes service in every namespace, and every
// Docker/Swarm agent is itself a named object on the network it serves. Nothing
// to configure, and no name that a user could remove.
//
// It must be a name the CLUSTER's own resolver answers directly. A hostname out
// of /etc/hosts would prove nothing — it never reaches the resolver being tested.
func resolverWitness() string {
	if k8sAvailable() {
		return "kubernetes.default"
	}
	if dockerAvailable() {
		if self, err := dockerSelf(); err == nil {
			for _, n := range []string{self.service, self.compose, self.name} {
				if n != "" {
					return n
				}
			}
		}
	}
	return ""
}

// localVersion is this agent's own version, baked into the image.
func localVersion() string {
	// What the embedder declared, when there is one. A gateway linking the agent
	// in has no /opt/plug/VERSION, and answering "unknown" is not cosmetic: the
	// CLI turns the answer into a cache path, asks this agent for that build's
	// digest, and refuses to run anything it cannot verify. An agent that cannot
	// name its version cannot serve a client at all.
	if v := strings.TrimSpace(os.Getenv(versionEnv)); v != "" {
		return v
	}
	if b, err := os.ReadFile(versionFile); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "unknown"
}

// signpostImage is the image the signpost container runs. It must carry the plug
// binary, since its entrypoint is /usr/local/bin/plug-agent (signpostArgs).
//
// Defaulting to the agent's OWN image is right for a standalone agent, which is
// that image, and wrong for an embedder, which is not: a gateway would create a
// signpost from its own image and the container would die instantly on a missing
// binary. Kubernetes never comes through here (it points a Service at the agent
// and creates no pod), so this is the Compose and Swarm answer.
func signpostImage(self string) string {
	if img := strings.TrimSpace(os.Getenv(signpostImageEnv)); img != "" {
		return img
	}
	return self
}

// The environment the SERVER process passes to a verb subprocess. A verb cannot
// read the embedder's Config, so anything the embedder decides has to arrive
// this way. Named here, beside the code that reads them, and set in serve.go.
const (
	versionEnv       = "PLUG_VERSION"
	signpostImageEnv = "PLUG_SIGNPOST_IMAGE"
	noSelfUpdateEnv  = "PLUG_NO_SELF_UPDATE"
	versionFile      = "/opt/plug/VERSION"
)

// portPair is one of a name's exposures: the port workloads dial, and the
// sshd-allocated agent port the signpost relays it to.
type portPair struct{ cluster, agent string }

// One name, one live session — and the proof of that must not depend on a
// signpost existing.
//
// The sshd bind USED to be the proof: one FIXED agent port per name, so a
// second session for the same name simply failed to bind and the kernel
// refused it, statelessly and always. Allocated ports removed that guarantee
// (port 0 always succeeds), and the replacement — read the existing signpost's
// target port, ask whether it still answers — only fires WHEN A SIGNPOST
// EXISTS. It does not right after the boot gc swept one, which is exactly when
// a still-live session is re-provisioning: both sessions then hold the name and
// take turns overwriting the signpost on every reconnect, each leaving the
// other silently unreachable.
//
// The lease records name → agent port when the name is served, independently of
// any signpost. It needs no cleanup to stay correct: it refuses only while the
// port it recorded still ANSWERS, and every agent port dies with its session.
// A var, not a const, only so tests can point it at a scratch directory —
// nothing in the agent ever reassigns it.
var nameLeaseDir = "/tmp/plug-names"

// leaseHolder returns the agent port recorded for name, "" when none is. name
// is a validated DNS label by the time it gets here (nameRe, at dispatch), so
// it cannot walk out of the directory.
func leaseHolder(name string) string {
	held, _ := readLease(name)
	return held
}

// leaseOrigin is where the session holding name connected FROM, "" when unknown
// (a lease written by an older agent, or a connection sshd told us nothing
// about). Only ever used to make a refusal say who to go and ask.
func leaseOrigin(name string) string {
	_, from := readLease(name)
	return from
}

// readLease parses the two-line lease: the agent port, then optionally where the
// session came from. One line is the format older agents wrote, and it reads
// back correctly as "port, origin unknown" — no migration, no version check.
func readLease(name string) (port, from string) {
	b, err := os.ReadFile(filepath.Join(nameLeaseDir, name))
	if err != nil {
		return "", ""
	}
	first, rest, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(first), strings.TrimSpace(rest)
}

// takeNameLease records this session as the owner of name, and where it reached
// us from. Best-effort: if the lease cannot be written, the backend checks still
// apply — we lose the extra guard, not the serve.
func takeNameLease(name, port string) {
	if err := os.MkdirAll(nameLeaseDir, 0o700); err != nil {
		return
	}
	body := port
	if from := sessionOrigin(); from != "" {
		body += "\n" + from
	}
	_ = os.WriteFile(filepath.Join(nameLeaseDir, name), []byte(body), 0o600)
}

// heldBy describes the session holding a name, for a refusal message. The agent
// nameHeldRefusal is the one refusal a client PARSES rather than just prints:
// heldPort in cli/servedmark.go looks for "agent port " inside it to decide
// whether the session holding the name is one of yours, and therefore whether it
// is safe to offer to stop it. Six copies of a format string, five of them out
// of sight of the sixth, and one losing its "(%s)" would break that offer on one
// backend and leave the other five working. The file already keeps its labels
// this way (parkedContainersLabel and its neighbours).
const nameHeldRefusal = "error: %q is already exposed by another live session (%s) - one -s per name at a time"

// port alone answers "is it taken"; the origin answers "by whom", which is the
// question the person reading it actually has. Silent when the lease predates
// this (an older agent wrote it) rather than inventing a source.
func heldBy(name, port string) string {
	if from := leaseOrigin(name); from != "" {
		return fmt.Sprintf("agent port %s, from %s", port, from)
	}
	return fmt.Sprintf("agent port %s", port)
}

// sessionOrigin is the far end of the SSH connection carrying this request, as
// sshd reports it: SSH_CLIENT is "<ip> <src-port> <dst-port>". The address is
// enough to tell a colleague's machine from your own, which is the whole
// question a "name already taken" raises.
//
// It is the address the AGENT sees, so behind NAT every developer shares one and
// it says only "somebody out there". A readable machine name would need the
// client to send one, which is a protocol change this does not make.
func sessionOrigin() string {
	f := strings.Fields(os.Getenv("SSH_CLIENT"))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func dropNameLease(name string) { _ = os.Remove(filepath.Join(nameLeaseDir, name)) }

// clearNameLeases wipes every lease at agent boot: a container that restarted
// took all of its forward ports down with it, so every lease it may still carry
// on a preserved writable layer names a dead port.
func clearNameLeases() { _ = os.RemoveAll(nameLeaseDir) }

// nameTaken reports whether a DIFFERENT live session already holds the name.
// held is the leased agent port, ours the ports this request brings, live
// probes one. Pure, so the decision is testable without a socket.
func nameTaken(held string, ours []portPair, live func(string) bool) bool {
	if held == "" {
		return false
	}
	for _, p := range ours {
		if p.agent == held {
			return false // our own lease, re-provisioned after a reconnect
		}
	}
	return live(held)
}

func serveName(name string, pairs []portPair) {
	// The port is named so the CLI can tell OUR OWN forgotten session from
	// someone else's: it records the port it holds a name on, and only a match
	// proves its record is the holder rather than a leftover on a recycled PID.
	if held := leaseHolder(name); nameTaken(held, pairs, agentPortLive) {
		answer(nameHeldRefusal, name, heldBy(name, held))
	}
	if len(pairs) > 0 {
		takeNameLease(name, pairs[0].agent)
	}
	if k8sAvailable() {
		k8sServe(name, pairs)
	}
	if dockerAvailable() {
		dockerServe(name, pairs)
	}
	// No orchestrator access: this agent cannot create the name. There is no
	// half-mode to fall back to — pre-declaring an alias per name is the exact
	// coordination -s removes — so say what is missing and let the session fail.
	answer("error: this agent has no orchestrator access, so it cannot create cluster names. " +
		"Mount /var/run/docker.sock on it (Compose/Swarm), or apply the Kubernetes RBAC (deploy/plug-k8s.yaml)")
}

// unserveName releases a name — and everything releasing it implies: the
// signpost goes, and whatever the session parked comes back.
//
// mine is the agent port the CALLER believes it holds the name on, and it is
// checked because holding a name is not forever. A laptop that sleeps past the
// keepalive loses its forward; the lease then names a dead port, so the next
// session to ask is granted the name — correctly. When the first laptop wakes
// and is eventually stopped, its teardown would otherwise delete the signpost
// the SECOND session is serving and restore a workload that session had parked,
// leaving it running and silently unreachable. A caller that sends no port, or
// a name nothing is leased for, is trusted: that is the pre-lease behaviour.
// unserveMayAct reports whether a caller releasing a name may act on it. held
// is what the lease records, mine is the port the caller believes it holds the
// name on. Trusting an empty either side keeps the pre-lease behaviour: no
// lease means nothing to arbitrate, and no port means a caller that predates
// the check.
func unserveMayAct(held, mine string) bool {
	return held == "" || mine == "" || held == mine
}

func unserveName(name, mine string) {
	if !unserveMayAct(leaseHolder(name), mine) {
		answer("ok reassigned") // another session owns it now — touch nothing
	}
	dropNameLease(name)
	if k8sAvailable() {
		k8sUnserve(name)
	}
	if dockerAvailable() {
		dockerUnserve(name)
	}
	answer("ok") // nothing provisioned here, nothing to drop
}

func gc() {
	clearNameLeases()
	if k8sAvailable() {
		k8sGC()
	}
	if dockerAvailable() {
		dockerGC()
	}
}

// ---- self-update: refresh THIS agent from its registry, per backend ----

func selfUpdate(decide func(string) (string, string, string)) {
	if k8sAvailable() {
		k8sSelfUpdate(decide)
	}
	if dockerAvailable() {
		self, err := dockerSelf()
		if err != nil {
			answer("error: %v", err)
		}
		if self.service != "" {
			swarmSelfUpdate(self, decide)
		}
		dockerPlainSelfUpdate(self, decide)
	}
	answer("error: this agent has no orchestrator access, so it cannot update itself — redeploy it by hand")
}

// ---- signpost: the process inside the alias-bearing container ----

// signpost relays <:port> to <target> (the agent, by container name — resolved
// per-connection so it survives agent restarts). It is the whole job of the
// signpost container: carry the DNS alias, hand the bytes to the agent's sshd
// remote-forward listener.
// How stubborn a signpost is about a failing Accept before it gives up: enough
// to ride out a burst of transient errors, few enough that a listener which is
// truly gone still terminates the process (and, on Swarm, gets restarted).
const (
	signpostAcceptRetries = 20
	signpostAcceptBackoff = 200 * time.Millisecond
)

func signpost(pairs []string) {
	for i := 0; i+1 < len(pairs); i += 2 {
		port, target := pairs[i], pairs[i+1]
		ln, err := net.Listen("tcp", ":"+port)
		if err != nil {
			fatal("signpost: %v", err)
		}
		fmt.Printf("signpost: :%s -> %s\n", port, target)
		go func() {
			// A signpost carries a name for a whole session — and, since one
			// signpost serves ALL of a name's ports, every port of it. Accept
			// can fail transiently (ECONNABORTED on a peer that vanished
			// mid-handshake, EMFILE under load): dying there would take the
			// cluster name down until the session ends, silently. So retry the
			// temporary ones and only give up when the listener itself is gone.
			fails := 0
			for {
				c, err := ln.Accept()
				if err != nil {
					var ne net.Error
					if errors.As(err, &ne) && ne.Timeout() {
						continue
					}
					if fails++; fails > signpostAcceptRetries {
						fatal("signpost: :%s stopped accepting: %v", port, err)
					}
					fmt.Fprintf(os.Stderr, "signpost: :%s accept failed (%v) — retrying\n", port, err)
					time.Sleep(signpostAcceptBackoff)
					continue
				}
				fails = 0
				go func() {
					t, err := net.DialTimeout("tcp", target, 5*time.Second)
					if err != nil {
						c.Close()
						return
					}
					relay(c, t)
				}()
			}
		}()
	}
	select {} // the listeners are the process
}

// One of three copies; the others are cli/internal/tun/netstack.go and cli/internal/tunnel/transport.go.
// The agent is a separate module, so sharing one would mean publishing a package
// for fifteen lines. Duplicated deliberately, and identical - the netstack copy
// had quietly lost its else branch below, which is the whole hazard of keeping
// three.
func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// ---- docker backend (sock mounted — the stack file's opt-in) ----

const dockerSock = "/var/run/docker.sock"

var dockerClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", dockerSock, 5*time.Second)
		},
	},
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// dnsNetworks are the docker networks with a working embedded DNS (so an alias
// resolves). The default `bridge`, `host` and `none` have none — an alias there
// is silently dead, so "only those" means the agent cannot carry a name at all.
var undnsNetwork = map[string]bool{"bridge": true, "host": true, "none": true}

// netKind classifies one of the agent's networks. app is false for the Swarm
// `ingress` routing mesh (a signpost must never touch it). attachable reports
// whether a STANDALONE container can join it: a bridge can, a Swarm overlay only
// if created `attachable: true`. overlay reports a Swarm overlay — the ONLY kind
// a Swarm SERVICE can join (a service cannot attach a node-local bridge). On
// inspect error, assume a plain joinable bridge — the proven Compose path must
// not regress.
func netKind(name string) (app, attachable, overlay bool) {
	var n struct {
		Driver     string `json:"Driver"`
		Ingress    bool   `json:"Ingress"`
		Attachable bool   `json:"Attachable"`
	}
	if _, err := dockerAPI("GET", "/networks/"+name, nil, &n); err != nil {
		return true, true, false
	}
	if n.Ingress {
		return false, false, false
	}
	if n.Driver == "overlay" {
		return true, n.Attachable, true
	}
	return true, true, false // bridge and the like
}

type netRef struct {
	name       string
	attachable bool
	overlay    bool
}

type selfInfo struct {
	name    string // agent container/task name — relay target for the container backend
	service string // agent's Swarm service name (empty off Swarm) — relay target for the service backend
	image   string
	imageID string   // resolved image id — what self-update compares a fresh pull against
	compose string   // compose service name (empty outside Compose) — the recreate hint
	nets    []netRef // application networks (overlay/bridge), minus ingress/host/none
}

// attachableNets: networks a standalone signpost CONTAINER can join (bridge, or
// an attachable overlay).
func (s selfInfo) attachableNets() []string {
	var out []string
	for _, n := range s.nets {
		if n.attachable {
			out = append(out, n.name)
		}
	}
	return out
}

// overlayNets: the overlays a Swarm SERVICE signpost can join (attachable or
// not). Node-local bridges are excluded — a service cannot attach one.
func (s selfInfo) overlayNets() []string {
	var out []string
	for _, n := range s.nets {
		if n.overlay {
			out = append(out, n.name)
		}
	}
	return out
}

// owner is the label that ties a signpost to this agent for gc. In Swarm it's
// the STABLE service name (the task/container name churns on restart); off Swarm
// it's the container name.
//
// It names the agent's ROLE, deliberately, and that is why it is no longer the
// only ownership the receipts carry. Every replica of one deployment reports the
// same owner, which is exactly what the check it feeds wants to know ("is this
// signpost some OTHER agent's?") and exactly what a receipt must not settle for
// ("which instance is holding this name right now?"). See sessionOwnerLabel.
func (s selfInfo) owner() string {
	if s.service != "" {
		return s.service
	}
	return s.name
}

// relayTarget is where a signpost sends the bytes: THIS agent, never the role it
// plays. Off Swarm that is the container name. On Swarm a task's container name
// IS its task name (<service>.<slot>.<task id>), which the overlay's embedded DNS
// answers with that one task's address - verified against a two-replica service,
// resolved from a peer task and from a standalone container alike.
//
// It used to return the Swarm SERVICE name here, i.e. the VIP, which load
// balances across every task. A session's remote-forward lives in exactly ONE of
// them, so past a single replica the VIP hands a share of the requests to a task
// that never heard of that session: not a slow name, a name that works one
// request in N. That was survivable only because -s refused to run at more than
// one replica; it stops being survivable the moment the agent is a package
// inside a gateway that scales.
func (s selfInfo) relayTarget() string { return s.name }

func signpostName(name string) string { return "plug-sp-" + name }

// Parking receipts — how a takeover is undone. The signpost created for the
// session carries, in its labels, exactly what was parked; unserve-name and the
// boot gc read it back and restore. Labels are immutable, so the receipt is
// written at signpost creation, before anything is parked.
const (
	parkedContainersLabel = "plug.parked.containers" // comma-joined container ids to restart
	parkedServiceLabel    = "plug.parked.service"    // Swarm service to scale back
	parkedReplicasLabel   = "plug.parked.replicas"   // …to this replica count

	// The signpost's own two, constants for the same reason as their neighbours
	// above and because they were the last literals left: six sites, two writing
	// and four READING, spread over six hundred lines. A typo in one of the four
	// readers makes another agent's signpost look like a residue nobody owns,
	// which is precisely what the boot sweep then removes.
	signpostLabel      = "plug.signpost"       // this container or service IS a signpost
	signpostOwnerLabel = "plug.signpost.owner" // and this session owns it
)

// sessionOwnerLabel records WHICH AGENT INSTANCE holds a name, as the address
// that proves it: "<this agent>:<the session's agent port>" - a container name, a
// Swarm task name or a pod IP, with the port that session's remote-forward
// answers on. Same key as a Docker/Swarm label and as a k8s annotation, and the
// k8s parking receipt carries it too (k8sReceipt.Owner).
//
// A receipt used to say only what was parked, never by whom, so a workload was
// restored by the ONE agent that parked it: its own boot gc read the receipt back
// and put things right. Where the agent is a dedicated container that is enough -
// it comes back under its own name and finds its own leftovers. Where the agent is
// a package inside a gateway that scales, the instance that parked a workload may
// simply never come back (scaled down, rescheduled, replaced), and then nobody
// restores: the real service stays parked at zero, or the name stays repointed,
// with a receipt no one feels entitled to act on.
//
// So the receipt names its owner, and any agent may restore one whose owner is
// DEMONSTRABLY gone - demonstrably meaning the address above no longer answers,
// which is the same question the serve path already asks about a name and the
// same evidence a session's forward gives when it dies with its process. Nothing
// here goes through the Host: leases are the agent's own business (see host.go on
// what belongs in that contract), and this way standalone plug gains from it too,
// where an agent redeployed under a different name can today restore nothing.
const sessionOwnerLabel = "plug.session.owner"

// sessionOwner is the value that label carries: this instance, and the port the
// session answers on. Empty when either half is unknown - an owner nobody can
// dial is not an owner, and reads as gone rather than as alive.
func sessionOwner(addr string, pairs []portPair) string {
	if addr == "" || len(pairs) == 0 || pairs[0].agent == "" {
		return ""
	}
	return net.JoinHostPort(addr, pairs[0].agent)
}

// sessionLive reports whether the session an owner address names is still there.
// One dial, from wherever the asking agent runs - a sibling replica's forward
// answers on the network exactly as this one's does, which 127.0.0.1 could never
// see. An empty address is nobody: gone.
func sessionLive(owner string) bool {
	if owner == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", owner, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ownerPort is the port half of an owner address, for the refusal message that
// names who holds a name.
func ownerPort(owner string) string {
	if _, p, err := net.SplitHostPort(owner); err == nil {
		return p
	}
	return owner
}

// owner is one RUNNING non-signpost container that answers to a name.
type owner struct {
	id   string
	name string // primary container name, for messages
}

// nameOwners returns the RUNNING NON-signpost containers name already resolves
// to on one of the given networks — i.e. the real service is deployed. Serving
// on top of one would only add the signpost to DNS round-robin (silent
// interception), so the caller either refuses or — takeover — parks them.
func nameOwners(name string, nets []string) []owner {
	mine := map[string]bool{}
	for _, n := range nets {
		mine[n] = true
	}
	var list []struct {
		Id              string            `json:"Id"`
		Names           []string          `json:"Names"`
		Labels          map[string]string `json:"Labels"`
		NetworkSettings struct {
			Networks map[string]struct{} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if _, err := dockerAPI("GET", "/containers/json", nil, &list); err != nil {
		return nil // can't tell — let Verify be the backstop
	}
	var owners []owner
	for _, c := range list {
		if c.Labels[signpostLabel] == "1" {
			continue // our own signposts don't count
		}
		primary := c.Id[:12]
		if len(c.Names) > 0 {
			primary = strings.TrimPrefix(c.Names[0], "/")
		}
		named := false
		for _, nm := range c.Names {
			if strings.TrimPrefix(nm, "/") == name {
				named = true
				break
			}
		}
		// The network alias is how a Compose service is reached by name — but
		// /containers/json returns its aliases as null (Docker only fills them
		// in on inspect), so a real service reached by its service-name alias
		// would slip through here. Inspect the candidates that share a network
		// with us to read the aliases reliably.
		if !named {
			onMine := false
			for net := range c.NetworkSettings.Networks {
				if mine[net] {
					onMine = true
					break
				}
			}
			named = onMine && containerHasAlias(c.Id, name, mine)
		}
		if named {
			owners = append(owners, owner{id: c.Id, name: primary})
		}
	}
	return owners
}

// swarmOwner describes the NON-signpost Swarm service that owns a name — the
// facts the takeover needs to park it (or to refuse precisely).
type swarmOwner struct {
	id       string
	name     string // the service's own Spec.Name
	replicas int
	global   bool
	viaAlias bool // owns the name as a network ALIAS, not as its service name
}

// scaleService sets a Swarm service's replica count, round-tripping the full
// Spec (the update API replaces the whole Spec — a partial one would strip
// fields) at the version the read returned.
func scaleService(idOrName string, replicas int) error {
	var s struct {
		Version struct {
			Index int `json:"Index"`
		} `json:"Version"`
		Spec map[string]any `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+idOrName, nil, &s); err != nil {
		return err
	}
	s.Spec["Mode"] = map[string]any{"Replicated": map[string]any{"Replicas": replicas}}
	_, err := dockerAPI("POST", "/services/"+idOrName+"/update?version="+strconv.Itoa(s.Version.Index), s.Spec, nil)
	return err
}

// signpostArgs renders the pairs as the signpost's argv: port target port target…
func signpostArgs(pairs []portPair, target string) []string {
	args := []string{"/usr/local/bin/plug-agent", "signpost"}
	for _, p := range pairs {
		args = append(args, p.cluster, target+":"+p.agent)
	}
	return args
}

// restoreContainerParked restarts whatever a previous session's signpost parked
// (its receipt label), then removes that signpost. No signpost → nothing to do.
// Restore-then-delete keeps the name resolving throughout: the real containers
// come back while the signpost still answers, then the signpost goes.
// agentPortLive reports whether something still listens on an agent-side port
// of THIS container — i.e. the session owning an existing signpost is alive.
// The sshd bind used to BE the collision check (one fixed port per name); with
// allocated ports, asking the port directly is what keeps "same name, live
// session" refused while a crashed session's leftover is still swept.
// It answers for THIS instance only, which is all the name lease needs (the
// lease file is this container's). Anything judging a name across instances asks
// sessionLive about the owner address instead.
func agentPortLive(port string) bool { return sessionLive("127.0.0.1:" + port) }

// signpostRelay digs the relay address out of an existing signpost's command:
// [bin, "signpost", p1, t1, p2, t2, …]. The FIRST pair is enough: all of a
// signpost's ports belong to one session, so one answering address means the
// session is alive. "" when the shape is not a signpost's.
//
// "<agent>:<port>" is an owner address (sessionOwnerLabel) written down before
// the label existed, which is why it is the fallback for a signpost an older
// agent created: an upgrade must not read every running session as a leftover.
func signpostRelay(cmd []string) string {
	if len(cmd) < 4 || cmd[1] != "signpost" {
		return ""
	}
	return cmd[3]
}

// signpostOwner is the instance holding a signpost's name: what it recorded, or,
// for a signpost an older agent created, the relay address in its command. Both
// answer the same question, and the older shape answers it just as well because
// the relay target has always been an address on the agent.
func signpostOwner(labels map[string]string, cmd []string) string {
	if o := labels[sessionOwnerLabel]; o != "" {
		return o
	}
	return signpostRelay(cmd)
}

func restoreContainerParked(name string) error {
	var insp struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if code, err := dockerAPI("GET", "/containers/"+signpostName(name)+"/json", nil, &insp); err != nil {
		if code == 404 {
			return nil
		}
		return err
	}
	// The receipt lives in the SIGNPOST's labels, and the signpost is deleted
	// two lines down. So a container that fails to restart here is lost to the
	// boot gc as well — there is nothing left telling anyone it was parked. Say
	// which ones, and do NOT delete the receipt that could still restore them.
	if failed := restartParkedContainers(insp.Config.Labels[parkedContainersLabel]); len(failed) > 0 {
		return fmt.Errorf("could not restart what this session parked (%s) — leaving the %s signpost in place "+
			"so its receipt survives; start them by hand, or restart the agent (its boot gc retries)",
			strings.Join(failed, ", "), name)
	}
	if code, err := dockerAPI("DELETE", "/containers/"+signpostName(name)+"?force=1", nil, nil); err != nil && code != 404 {
		return err
	}
	return nil
}

// restartParkedContainers starts every id in a receipt and returns the ids it
// could NOT bring back.
//
// A container removed meanwhile (404) or already running (304) is not a
// failure — the workload is where it should be either way. Anything else is:
// the host port taken over in the meantime, a daemon hiccup, a 409. Those used
// to be swallowed, and since the caller then deleted the signpost carrying the
// receipt, the workload stayed down with nothing left to say it had been parked.
func restartParkedContainers(receipt string) []string {
	var failed []string
	for _, id := range strings.Split(receipt, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if code, err := dockerAPI("POST", "/containers/"+id+"/start", nil, nil); err != nil &&
			code != 404 && code != 304 {
			failed = append(failed, id)
		}
	}
	return failed
}

// swarmServe runs the signpost as a Swarm SERVICE. A service joins the stack's
// overlay whether or not it is `attachable` — the whole reason this backend
// exists - and carries the alias there, relaying to the agent TASK that holds
// the session (relayTarget), never to the service VIP that would spread it.
// pinnedImage returns img pinned to the digest the LOCAL engine knows for its
// repository (repo@sha256:…), or img unchanged when it has none (an image built
// locally, never pulled). Swarm-specific on purpose: a bare tag in a service
// spec makes the manager resolve it against the registry at create time, and on
// a plugged workstation that lookup rides the session's own DNS detour —
// measured at three ~31s registry round-trips per signpost, which WAS the whole
// -s wait. The signpost's image is the agent's own, so the local digest is
// always the right answer and costs no network at all (0.6s to Running,
// measured, with a session active). `plug update` writes bare tags (it drops
// the digest to move the deployment), so without this every updated agent
// reintroduces the wait.
func pinnedImage(img string) string {
	base := img
	if i := strings.Index(base, "@sha256:"); i > 0 {
		base = base[:i] // re-pin from local knowledge, not from a stale spec
	}
	var info struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if _, err := dockerAPI("GET", "/images/"+base+"/json", nil, &info); err != nil {
		return img
	}
	if d := digestFor(base, info.RepoDigests); d != "" {
		return d
	}
	return img
}

// digestFor picks the RepoDigest matching img's repository — an image can carry
// digests for several repos (retagged), and a digest for another repo would
// make the manager pull a stranger.
func digestFor(img string, repoDigests []string) string {
	repo := img
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	for _, d := range repoDigests {
		if strings.HasPrefix(d, repo+"@sha256:") {
			return d
		}
	}
	return ""
}

// restoreServiceParked scales back whatever a previous session's signpost
// service parked (its receipt labels), then removes that signpost. Scale-back
// first, delete second — the name keeps resolving throughout.
// ---- linger: an unserved name keeps its address warm for a relaunch ----
//
// Callers cache a name's address for as long as the DNS said they may — and
// Docker's embedded DNS says 600 seconds, hard-coded. A gateway on a resolver
// that honours TTLs (Netty does) keeps dialling the old address for up to ten
// minutes after every Ctrl-C→relaunch, because deleting and recreating the
// signpost hands the name a fresh VIP. No TTL on plug's side can shorten a TTL
// served by Docker; the only fix that reaches every caller is an address that
// does not change. So a cleanly-unserved signpost is no longer deleted: it
// LINGERS — still resolving, refusing connections like any stopped service —
// and the next serve of that name takes it over in place, address intact.
//
// Only Swarm services and Kubernetes Services can linger: both can be retargeted
// in place. A plain container's relay target is baked into its entrypoint, so a
// new session means a new container and there is nothing to keep — that shape
// keeps today's delete (and Docker's own IPAM often re-hands the same IP).
//
// A signpost carrying a parking receipt never lingers: deleting it is what
// scales the parked workload back up, and an address is not worth a deployed
// service left at zero replicas.

// lingerGrace is how long an unserved name stays warm before the GC reaps it.
// Derived, not felt: it must outlive the 600s TTL Docker's DNS handed to every
// caller — otherwise the linger protects nothing — and fifteen minutes gives
// margin without keeping dead names resolving for hours.
const lingerGrace = 15 * time.Minute

// lingerLabel stamps WHEN the name was unserved (unix seconds) — as a Swarm
// service label, and verbatim as a k8s annotation key.
const lingerLabel = "plug.linger.since"

func lingerStamp() string { return strconv.FormatInt(time.Now().Unix(), 10) }

// lingerExpired reports whether a stamp is past the grace. Empty means "not
// lingering" (a live session's signpost, or a crash leftover — the GC's other
// rules own those). An unreadable stamp reads as expired: reaping is the honest
// direction for a label something has mangled.
func lingerExpired(stamp string, now time.Time) bool {
	if stamp == "" {
		return false
	}
	n, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return true
	}
	return now.Sub(time.Unix(n, 0)) > lingerGrace
}

// sweepExpiredServiceLingers reaps every lingering signpost past its grace —
// called from serve, because boot is the only other moment the agent runs any
// code, and an agent that never restarts must not keep dead names resolving
// for ever. One label-filtered list; best-effort like the gc.
func sweepExpiredServiceLingers() {
	var slist []struct {
		ID   string `json:"ID"`
		Spec struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Spec"`
	}
	f := `{"label":["` + signpostLabel + `=1"]}`
	if _, err := dockerAPI("GET", "/services?filters="+urlEscape(f), nil, &slist); err != nil {
		return
	}
	now := time.Now()
	for _, s := range slist {
		if lingerExpired(s.Spec.Labels[lingerLabel], now) {
			_, _ = dockerAPI("DELETE", "/services/"+s.ID, nil, nil)
		}
	}
}

// markServiceLinger stamps the signpost service instead of deleting it. The
// update round-trips the FULL spec (Docker's update replaces, not merges) with
// only the label added.
func markServiceLinger(name string) error {
	var s struct {
		ID      string `json:"ID"`
		Version struct {
			Index uint64 `json:"Index"`
		} `json:"Version"`
		Spec map[string]any `json:"Spec"`
	}
	if code, err := dockerAPI("GET", "/services/"+signpostName(name), nil, &s); err != nil {
		if code == 404 || code == 503 {
			return nil
		}
		return err
	}
	labels, _ := s.Spec["Labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels[lingerLabel] = lingerStamp()
	s.Spec["Labels"] = labels
	_, err := dockerAPI("POST", fmt.Sprintf("/services/%s/update?version=%d", s.ID, s.Version.Index), s.Spec, nil)
	return err
}

func restoreServiceParked(name string) error {
	var s struct {
		Spec struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Spec"`
	}
	if code, err := dockerAPI("GET", "/services/"+signpostName(name), nil, &s); err != nil {
		if code == 404 || code == 503 { // absent, or not a manager (no service shape here)
			return nil
		}
		return err
	}
	// No receipt → nothing to put back → the signpost LINGERS instead of dying:
	// the name keeps its VIP for a quick relaunch (see the linger block above).
	// With a receipt the address yields to the workload, exactly as before.
	if s.Spec.Labels[parkedServiceLabel] == "" {
		return markServiceLinger(name)
	}
	// Same rule as the container shape: the receipt is in the SIGNPOST's labels
	// and the signpost goes next, so a scale-back that failed must stop us —
	// otherwise the service stays at 0 replicas with nothing left recording that
	// a session put it there.
	if err := scaleBackParkedService(s.Spec.Labels); err != nil {
		return fmt.Errorf("could not scale %q back up (%v) — leaving the %s signpost in place so its "+
			"receipt survives; scale it by hand, or restart the agent (its boot gc retries)",
			s.Spec.Labels[parkedServiceLabel], err, name)
	}
	if code, err := dockerAPI("DELETE", "/services/"+signpostName(name), nil, nil); err != nil && code != 404 {
		return err
	}
	return nil
}

// scaleBackParkedService restores the replica count a receipt recorded,
// best-effort: a service removed meanwhile is fine.
func scaleBackParkedService(labels map[string]string) error {
	svc := labels[parkedServiceLabel]
	if svc == "" {
		return nil
	}
	n, err := strconv.Atoi(labels[parkedReplicasLabel])
	if err != nil || n < 1 {
		n = 1
	}
	return scaleService(svc, n)
}

// dockerGC sweeps, at agent boot, THIS agent's own orphaned signposts (an agent
// restart leaves its sessions' signposts running). A signpost is ours if its
// owner label is our current name OR its owner container no longer exists — the
// latter covers Swarm, where the agent's container name churns on restart, so
// the old signposts' owner never equals the new name but their owner container
// is gone. This leaves a CO-LOCATED other agent's live signposts (owner still
// running) untouched — the pure-shared-network scan used to wipe those.
// gcNote reports a boot-gc failure on stderr — the container's log, which is
// where anyone hunting "why is my service still scaled to 0" will look. gc is
// best-effort by design (a crashed session's leftovers), but best-effort must
// not mean invisible: its whole job is restoring workloads a dead session
// parked.
func gcNote(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "plug-agent gc: "+format+"\n", a...)
}

// ownerAlive reports whether the owner agent still exists. Off a Swarm manager
// (swarm=false) only containers can be owners AND /services/* answers 503, so we
// must NOT consult serviceExists there — otherwise its non-404 makes ownerAlive
// unconditionally true and the orphan sweep never fires (the Compose regression
// the review caught). An empty owner counts as gone.
func ownerAlive(name string, swarm bool) bool {
	if name == "" {
		return false
	}
	if containerExists(name) {
		return true
	}
	return swarm && serviceExists(name)
}

func urlEscape(s string) string {
	return strings.NewReplacer("{", "%7B", "}", "%7D", `"`, "%22", "[", "%5B", "]", "%5D", ",", "%2C", ":", "%3A", "=", "%3D").Replace(s)
}

// ---- kubernetes backend (RBAC applied — part of deploy/plug-k8s.yaml) ----

const k8sSA = "/var/run/secrets/kubernetes.io/serviceaccount"

// readAPIReply turns one orchestrator answer into (status, error). Shared by the
// Docker and Kubernetes callers because they had the same twenty lines and had
// stopped agreeing on them: the Docker one returned the decode error, the
// Kubernetes one discarded it with `_ =`. An answer the agent could not read
// therefore came back from Kubernetes as "200, everything fine" with out left at
// its zero value, and the caller acted on that: a Service read as having no
// selector and no ports is indistinguishable from a Service that really has
// none, and the repoint that follows is made on nothing.
//
// withBody says whether the raw response is worth quoting when the API refuses.
// Docker's errors are often a bare string with no JSON around them; Kubernetes
// always sends a Status object, whose message is the useful part and whose full
// text is a wall.
func readAPIReply(status int, statusText string, data []byte, out any, withBody bool) (int, error) {
	if out != nil && status < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			return status, fmt.Errorf("the API answered %s and this could not be read: %w", statusText, err)
		}
	}
	if status >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if withBody {
			return status, fmt.Errorf("%s", firstNonEmpty(e.Message, strings.TrimSpace(string(data)), statusText))
		}
		return status, fmt.Errorf("%s", firstNonEmpty(e.Message, statusText))
	}
	return status, nil
}

const k8sManaged = "app.kubernetes.io/managed-by"

// k8sParkedAnn is the k8s parking receipt: the takeover repoints the REAL
// Service at the agent and stores its original selector+ports here, on the
// object itself — so the restore (unserve or boot gc) survives any agent crash.
// The annotation is written ONLY when absent: a crashed takeover session leaves
// the Service already pointing at plug, and re-saving would overwrite the
// original with {app: plug}, losing the way back.
const k8sParkedAnn = "plug.softwarity.io/parked"

// k8sReceipt is what the annotation stores: everything the restore re-patches,
// and who owes it. Owner is a sessionOwnerLabel address: the pod that parked
// this Service and the port its session answers on, so that ANY agent can tell
// whether the parking is still someone's business. Absent on a receipt an older
// agent wrote, which reads as an owner that is gone: exactly the old behaviour,
// where the gc restored whatever it found parked.
type k8sReceipt struct {
	Selector map[string]string `json:"selector"`
	Ports    json.RawMessage   `json:"ports"`
	Owner    string            `json:"owner,omitempty"`
}

// What writing the endpoints leaves us to do. Split out of k8sPointAtSelf so the
// decision can be asserted: that function calls answer(), which exits, and this
// is the branch that keeps `plug update` from breaking clusters. plug update
// moves the IMAGE and never the manifest, so an agent that started refusing on
// an old RBAC would take -s away from a deployment that worked, on an upgrade
// nobody asked questions about.
type endpointsVerdict int

const (
	endpointsDone     endpointsVerdict = iota // written, the name reaches this pod
	endpointsFallback                         // no grant: point the name the old way
	endpointsFatal                            // a cluster refusing the write, not an old deployment
)

func endpointsOutcome(code int, err error) endpointsVerdict {
	switch {
	case err == nil:
		return endpointsDone
	case code == 403:
		// The one shape that means "deployed before the endpoints grant". Every
		// other failure is this cluster saying no to something it should allow.
		return endpointsFallback
	default:
		return endpointsFatal
	}
}

// selectorPatch builds the merge-patch value that REPLACES a selector: RFC 7386
// merges maps key-by-key, so every key of the current selector that the target
// doesn't carry must be explicitly nulled or it would survive the patch (and a
// half-merged selector matches nothing).
func selectorPatch(target, current map[string]string) map[string]any {
	p := map[string]any{}
	for k := range current {
		p[k] = nil
	}
	for k, v := range target {
		p[k] = v
	}
	return p
}

// sweepExpiredK8sLingers is the serve-time reap, Swarm's twin: an agent that
// never restarts must not keep dead names resolving for ever.
func sweepExpiredK8sLingers(ns string) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if _, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services?labelSelector="+urlEscape(k8sManaged+"=plug"), nil, &list); err != nil {
		return
	}
	now := time.Now()
	for _, s := range list.Items {
		if stamp := s.Metadata.Annotations[lingerLabel]; stamp != "" && lingerExpired(stamp, now) {
			k8sDropName(ns, s.Metadata.Name)
		}
	}
}
