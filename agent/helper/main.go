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
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
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
func preflight() {
	if k8sAvailable() || dockerAvailable() {
		return
	}
	fatal("plug-agent: no orchestrator access, so this agent cannot create cluster names.\n" +
		"  Docker / Compose / Swarm: mount /var/run/docker.sock into the agent\n" +
		"      volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]\n" +
		"      (on Swarm, also run it as a service on a MANAGER node)\n" +
		"  Kubernetes: apply the RBAC that lets it manage Services\n" +
		"      kubectl apply -f deploy/plug-k8s.yaml\n" +
		"  Full stack files: " + docURL(docHome))
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// answer prints the one-line protocol reply the CLI parses, and exits 0 — the
// reply itself carries success or failure ("error: …"), so the SSH exit status
// stays out of the contract (old CLIs never call us; old shells said 127).
func answer(format string, a ...any) {
	fmt.Printf(format+"\n", a...)
	os.Exit(0)
}

// A DNS label the way BOTH backends accept it: RFC 1035 (leading letter) so a
// k8s Service object is valid too — docker would take a leading digit, k8s
// won't, and -s must behave the same whichever backend answers.
var nameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func dispatch(cmd []string) {
	if len(cmd) == 0 {
		fatal("plug-agent: this user runs the tunnel and the -s verbs; there is no shell")
	}
	switch cmd[0] {
	case "serve-name":
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
	case "unserve-name":
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
	case "info":
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
		if img != "" {
			answer("version=%s backend=%s image=%s", ver, backend, img)
		}
		answer("version=%s backend=%s", ver, backend)
	case "check-update":
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
	case "resolve":
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
	case "self-update":
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
	default:
		answer("error: unknown command %q", cmd[0])
	}
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
	if b, err := os.ReadFile("/opt/plug/VERSION"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "unknown"
}

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
		answer("error: %q is already exposed by another live session (%s) — one -s per name at a time", name, heldBy(name, held))
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

// k8sAgentDeployment finds the agent's own Deployment (label app=plug, this
// namespace) and the container running the plug image — a pod may carry
// sidecars, and patching the wrong one would be silent.
func k8sAgentDeployment() (depName, container, img string, code int, err error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name  string `json:"name"`
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	code, err = k8sAPI("GET", "/apis/apps/v1/namespaces/"+k8sNamespace()+"/deployments?labelSelector=app%3Dplug", nil, &list)
	if err != nil || len(list.Items) == 0 {
		return "", "", "", code, err
	}
	dep := list.Items[0]
	for _, c := range dep.Spec.Template.Spec.Containers {
		if strings.Contains(c.Image, "plug") {
			return dep.Metadata.Name, c.Name, c.Image, code, nil
		}
	}
	if len(dep.Spec.Template.Spec.Containers) == 1 {
		c := dep.Spec.Template.Spec.Containers[0]
		return dep.Metadata.Name, c.Name, c.Image, code, nil
	}
	return dep.Metadata.Name, "", "", code, nil
}

// k8sSelfUpdate updates the agent's own Deployment. A pinned RELEASE tag is
// rewritten to the newest release — a rolling restart alone would re-pull the
// same pin forever. A moving tag keeps the restart-only path (the annotation
// patch `kubectl rollout restart` uses), which makes the node re-pull it per
// its imagePullPolicy — Always in the official manifest.
func k8sSelfUpdate(decide func(string) (string, string, string)) {
	ns := k8sNamespace()
	name, container, img, code, err := k8sAgentDeployment()
	if err != nil {
		if code == 403 {
			answer("error: the deployed RBAC predates self-update — re-apply deploy/plug-k8s.yaml (it adds the deployments grant), or run: kubectl -n %s rollout restart deployment plug", ns)
		}
		answer("error: finding the agent deployment: %v", err)
	}
	if name == "" {
		answer("error: no deployment labeled app=plug in namespace %s — restart the agent's workload by hand", ns)
	}

	target, plan, note := decide(img)
	if plan == planCurrent {
		answer("current %s", note)
	}
	// The restart annotation goes on either way: with a new image it makes the
	// rollout unambiguous, with a moving tag it IS the update.
	template := map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"plug.softwarity.io/restartedAt": time.Now().UTC().Format(time.RFC3339)},
	}}
	if plan == planRetarget && container != "" {
		template["spec"] = map[string]any{"containers": []map[string]any{
			{"name": container, "image": target},
		}}
	}
	patch := map[string]any{"spec": map[string]any{"template": template}}
	if code, err := k8sMergePatch("/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, patch); err != nil {
		if code == 403 {
			answer("error: the deployed RBAC predates self-update — re-apply deploy/plug-k8s.yaml (it adds the deployments grant), or run: kubectl -n %s rollout restart deployment %s", ns, name)
		}
		answer("error: updating deployment %s: %v", name, err)
	}
	answer("updating deployment %s (namespace %s) — %s", name, ns, note)
}

// swarmSelfUpdate rolls the agent's own service. A pinned RELEASE tag is moved
// to the newest release (that is the whole point — re-resolving a pin can only
// ever return the same image); a moving tag is left as it is and merely
// re-resolved. Either way the pinned digest is dropped from the image (stack
// deploy pins one — with it, no update ever changes anything) and ForceUpdate
// rolls the task even when the digest comes back unchanged.
func swarmSelfUpdate(self selfInfo, decide func(string) (string, string, string)) {
	if !swarmManager() {
		answer("error: the agent's node is not a swarm manager — from one, run: docker service update --image %s %s",
			retargetImageOnly(self.image), self.service)
	}
	var s struct {
		ID      string `json:"ID"`
		Version struct {
			Index int `json:"Index"`
		} `json:"Version"`
		Spec map[string]any `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+self.service, nil, &s); err != nil {
		answer("error: reading service %s: %v", self.service, err)
	}
	tt, _ := s.Spec["TaskTemplate"].(map[string]any)
	if tt == nil {
		answer("error: service %s has no task template", self.service)
	}
	img := self.image
	if cs, _ := tt["ContainerSpec"].(map[string]any); cs != nil {
		if is, _ := cs["Image"].(string); is != "" {
			img = is
		}
		// The digest is NOT stripped here: a digest-only pin must reach the
		// decision, where it means "release pin", not the `latest` a naively
		// stripped repo reads as.
	}
	target, plan, note := decide(img)
	// Already the newest release: say so NOW rather than roll the task and let
	// the CLI poll 90s for a version that cannot change. The tag is a pin and
	// the registry has nothing above it — there is nothing to re-resolve.
	if plan == planCurrent {
		answer("current %s", note)
	}
	if cs, _ := tt["ContainerSpec"].(map[string]any); cs != nil {
		cs["Image"] = target
	}
	fu, _ := tt["ForceUpdate"].(float64)
	tt["ForceUpdate"] = int(fu) + 1
	if _, err := dockerAPI("POST", "/services/"+s.ID+"/update?version="+strconv.Itoa(s.Version.Index), s.Spec, nil); err != nil {
		answer("error: updating service %s: %v", self.service, err)
	}
	answer("updating service %s — %s, and the task rolls", self.service, note)
}

// dockerPlainSelfUpdate (Compose / plain `docker run`): pull the deployed tag
// and compare image ids. A container cannot recreate ITSELF, so when something
// newer landed the answer carries the one command the caller runs — with the
// image already local, that recreate is instant.
func dockerPlainSelfUpdate(self selfInfo, decide func(string) (string, string, string)) {
	ver := localVersion()
	img := self.image
	if strings.HasPrefix(img, "sha256:") {
		answer("error: the agent was started from an image ID, not a tag — recreate it from a tag (softwarity/plug:latest) so updates can pull")
	}
	target, plan, note := decide(img)
	if plan == planCurrent {
		answer("current %s", note)
	}
	// Pull the image the deployment should end up on — the new tag when the pin
	// moves, the same one when it is a moving tag. Either way it is local by the
	// time the operator runs the recreate, so that step is instant.
	if err := dockerPull(target); err != nil {
		answer("current v%s — could not pull %s (%v)", ver, target, err)
	}
	// A retarget always warrants the recreate: the container runs an image the
	// deployment no longer names. Only a moving tag can legitimately turn out
	// to be unchanged.
	if plan == planResolve {
		var pulled struct {
			Id string `json:"Id"`
		}
		if _, err := dockerAPI("GET", "/images/"+target+"/json", nil, &pulled); err != nil {
			answer("current v%s — could not inspect %s after the pull (%v)", ver, target, err)
		}
		if pulled.Id == self.imageID {
			answer("current v%s — image %s unchanged", ver, target)
		}
	}
	// A container cannot recreate itself. Hand back the exact command — and
	// when the tag moved, the compose file has to be edited first, or the next
	// `up` would put the old pin straight back.
	how := "recreate the agent container with " + target
	if self.compose != "" {
		how = "docker compose up -d " + self.compose
		if plan == planRetarget {
			how = "set the plug service's image to " + target + " in your compose file, then: " + how
		}
	} else if plan == planRetarget {
		how = "recreate the agent container from " + target
	}
	answer("pulled %s — %s; the agent cannot recreate its own container: %s", target, note, how)
}

// dockerPull pulls ref (name[:tag]) through the daemon, draining the progress
// stream — the API answers 200 and reports failures IN the stream. Its own
// client: the pull outlives the 20s the control-plane calls are bounded to.
func dockerPull(ref string) error {
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	cl := &http.Client{Timeout: 3 * time.Minute, Transport: dockerClient.Transport}
	resp, err := cl.Post("http://docker/images/create?fromImage="+url.QueryEscape(name)+"&tag="+url.QueryEscape(tag), "text/plain", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("%s", firstNonEmpty(e.Message, strings.TrimSpace(string(data)), resp.Status))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("%s", m.Error)
		}
	}
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

func dockerAvailable() bool {
	_, err := os.Stat(dockerSock)
	return err == nil
}

var dockerClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", dockerSock, 5*time.Second)
		},
	},
}

func dockerAPI(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://docker"+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := dockerClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("%s", firstNonEmpty(e.Message, strings.TrimSpace(string(data)), resp.Status))
	}
	return resp.StatusCode, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// containerID finds THIS container's real id from the cgroup/mountinfo the
// kernel exposes — authoritative, unlike the hostname (which a stack can
// override, and which would then GET a DIFFERENT container's inspect). Falls
// containerIDFromMount reads THIS container's id from the Docker-bind-mounted
// paths in mountinfo (`…/containers/<id>/{resolv.conf,hostname,hosts}`). The
// segment is anchored on `/containers/<64-hex>/`, NOT any 64-hex — the overlay
// layer hashes elsewhere in mountinfo are also 64-hex and must not be picked.
// "" when the pattern isn't present (returns to the hostname).
func containerIDFromMount() string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`/containers/([0-9a-f]{64})/`).FindSubmatch(b); m != nil {
		return string(m[1])
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
func (s selfInfo) owner() string {
	if s.service != "" {
		return s.service
	}
	return s.name
}

// relayTarget is where a signpost sends the bytes: the agent's Swarm service
// (VIP → the agent task holding the session; assumes the usual single replica)
// or, off Swarm, the container name.
func (s selfInfo) relayTarget() string {
	if s.service != "" {
		return s.service
	}
	return s.name
}

// dockerSelf identifies OUR container. The hostname is the short container id by
// default and resolves directly — the proven path. Only if that GET fails (a
// stack that overrode the hostname) do we fall back to the authoritative id
// parsed from mountinfo.
func dockerSelf() (selfInfo, error) {
	var s selfInfo
	var insp struct {
		Name   string `json:"Name"`
		Image  string `json:"Image"` // the resolved image ID (sha256:…)
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			Networks map[string]struct{} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	id, _ := os.Hostname()
	_, err := dockerAPI("GET", "/containers/"+id+"/json", nil, &insp)
	if err != nil {
		if mid := containerIDFromMount(); mid != "" && mid != id {
			_, err = dockerAPI("GET", "/containers/"+mid+"/json", nil, &insp)
		}
		if err != nil {
			return s, fmt.Errorf("cannot identify the agent container: %v", err)
		}
	}
	s.name = strings.TrimPrefix(insp.Name, "/")
	s.image = insp.Config.Image
	s.imageID = insp.Image
	s.compose = insp.Config.Labels["com.docker.compose.service"]
	s.service = insp.Config.Labels["com.docker.swarm.service.name"]
	for n := range insp.NetworkSettings.Networks {
		if undnsNetwork[n] {
			continue
		}
		if app, att, ov := netKind(n); app {
			s.nets = append(s.nets, netRef{n, att, ov})
		}
	}
	return s, nil
}

// swarmManager reports whether this node can create Swarm services (a manager).
// When it can, the signpost is a SERVICE (joins non-attachable overlays too),
// not a standalone container.
func swarmManager() bool {
	var info struct {
		Swarm struct {
			ControlAvailable bool `json:"ControlAvailable"`
		} `json:"Swarm"`
	}
	if _, err := dockerAPI("GET", "/info", nil, &info); err != nil {
		return false
	}
	return info.Swarm.ControlAvailable
}

func signpostName(name string) string { return "plug-sp-" + name }

// Parking receipts — how a takeover is undone. The signpost created for the
// session carries, in its labels, exactly what was parked; unserve-name and the
// boot gc read it back and restore. Labels are immutable, so the receipt is
// written at signpost creation, before anything is parked.
const (
	parkedContainersLabel = "plug.parked.containers" // comma-joined container ids to restart
	parkedServiceLabel    = "plug.parked.service"    // Swarm service to scale back
	parkedReplicasLabel   = "plug.parked.replicas"   // …to this replica count
)

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
		if c.Labels["plug.signpost"] == "1" {
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

// containerHasAlias reports whether the container answers to name on one of our
// networks — read from inspect, where the aliases (and DNSNames) are populated,
// unlike the container list.
func containerHasAlias(id, name string, mine map[string]bool) bool {
	var insp struct {
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases  []string `json:"Aliases"`
				DNSNames []string `json:"DNSNames"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if _, err := dockerAPI("GET", "/containers/"+id+"/json", nil, &insp); err != nil {
		return false
	}
	for net, ep := range insp.NetworkSettings.Networks {
		if !mine[net] {
			continue
		}
		for _, a := range ep.Aliases {
			if a == name {
				return true
			}
		}
		for _, d := range ep.DNSNames {
			if d == name {
				return true
			}
		}
	}
	return false
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

// swarmNameOwner returns the NON-signpost Swarm service that already owns name
// — by its service name (the cluster-wide resolvable name) or a network alias —
// or nil. GET /services lists the WHOLE cluster from a manager, so it also
// catches a real service whose tasks run on other nodes (which nameOwners'
// container scan cannot see). Serving on top would shadow it in DNS.
func swarmNameOwner(name string, self selfInfo) *swarmOwner {
	// Scope to networks WE are on: a service on an overlay we don't share doesn't
	// resolve for our workloads, so serving `name` on our overlay would not shadow
	// it (the container path already scopes this way). `mine` holds our overlays'
	// names AND ids — a service spec's Network Target may be either. If an id
	// lookup fails we can't scope reliably, so fall back to the old cluster-wide
	// check (over-refuse safely) rather than risk missing a real collision.
	mine := map[string]bool{}
	scoped := true
	for _, n := range self.overlayNets() {
		mine[n] = true
		var ni struct {
			Id string `json:"Id"`
		}
		if _, err := dockerAPI("GET", "/networks/"+n, nil, &ni); err == nil && ni.Id != "" {
			mine[ni.Id] = true
		} else {
			scoped = false
		}
	}
	var list []struct {
		ID   string `json:"ID"`
		Spec struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
			Mode   struct {
				Replicated struct {
					Replicas int `json:"Replicas"`
				} `json:"Replicated"`
				Global *struct{} `json:"Global"`
			} `json:"Mode"`
			TaskTemplate struct {
				Networks []struct {
					Target  string   `json:"Target"`
					Aliases []string `json:"Aliases"`
				} `json:"Networks"`
			} `json:"TaskTemplate"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services", nil, &list); err != nil {
		return nil // can't tell — Verify is the backstop
	}
	for _, s := range list {
		if s.Spec.Labels["plug.signpost"] == "1" {
			continue // our own signpost services don't count
		}
		shared := !scoped // if we couldn't resolve our net ids, assume shared (safe)
		if scoped {
			for _, n := range s.Spec.TaskTemplate.Networks {
				if mine[n.Target] {
					shared = true
					break
				}
			}
		}
		if !shared {
			continue // no shared network — it can't shadow our name
		}
		// The service's own name resolves on every network it's attached to, and
		// an alias resolves on its network — either collides once a network is shared.
		owns, viaAlias := s.Spec.Name == name, false
		if !owns {
			for _, n := range s.Spec.TaskTemplate.Networks {
				for _, a := range n.Aliases {
					if a == name {
						owns, viaAlias = true, true
						break
					}
				}
			}
		}
		if owns {
			return &swarmOwner{
				id:       s.ID,
				name:     s.Spec.Name,
				replicas: s.Spec.Mode.Replicated.Replicas,
				global:   s.Spec.Mode.Global != nil,
				viaAlias: viaAlias,
			}
		}
	}
	return nil
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

// dockerServe picks the signpost shape. The agent runs as a Swarm SERVICE (it
// has a service name) AND this node can create services (a manager) → the
// signpost is a SERVICE, which joins the stack's overlay whether or not it is
// attachable. Otherwise (Compose, plain `docker run`, or a non-manager) it is a
// standalone CONTAINER, which needs a bridge or an attachable overlay.
func dockerServe(name string, pairs []portPair) {
	self, err := dockerSelf()
	if err != nil {
		answer("error: %v", err)
	}
	if self.service != "" && swarmManager() {
		swarmServe(name, pairs, self)
	}
	containerServe(name, pairs, self)
}

// signpostArgs renders the pairs as the signpost's argv: port target port target…
func signpostArgs(pairs []portPair, target string) []string {
	args := []string{"/usr/local/bin/plug-agent", "signpost"}
	for _, p := range pairs {
		args = append(args, p.cluster, target+":"+p.agent)
	}
	return args
}

// containerServe runs the signpost as a standalone container — needs a network
// it can actually join (a bridge, or an attachable overlay).
func containerServe(name string, pairs []portPair, self selfInfo) {
	nets := self.attachableNets()
	if len(nets) == 0 {
		// Nothing a standalone container can join (only bridge/host, or a
		// non-attachable overlay off a Swarm manager), so the signpost has
		// nowhere to carry the alias.
		answer("error: the agent is on no network a signpost can join — put it on the " +
			"application network (an attachable overlay, or the Compose network your services share)")
	}
	// A signpost already carrying this name may belong to a LIVE session — its
	// relay port still answers on this agent — and then the name is taken; a
	// dead port is a crashed session's leftover, swept below.
	var insp struct {
		Config struct {
			Entrypoint []string          `json:"Entrypoint"`
			Labels     map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if code, err := dockerAPI("GET", "/containers/"+signpostName(name)+"/json", nil, &insp); err == nil && code == 200 {
		// Whose signpost is this? A container name is HOST-wide, so two agents
		// on one host — two Compose stacks, each with its own plug — collide on
		// `plug-sp-<name>`. The liveness probe below cannot tell them apart: it
		// dials 127.0.0.1 in OUR netns, where the other agent's port never
		// answers, so its LIVE signpost reads as a leftover and gets swept. The
		// gc has always checked this label; the serve path never did.
		if o := insp.Config.Labels["plug.signpost.owner"]; o != "" && o != self.owner() && ownerAlive(o, false) {
			answer("error: %q is served here by another plug agent (%s), which is still running — "+
				"two agents on one host cannot both own a name. Use a different name, or stop that agent.", name, o)
		}
		if ap := signpostAgentPort(insp.Config.Entrypoint); ap != "" && agentPortLive(ap) {
			answer("error: %q is already exposed by another live session (%s) — one -s per name at a time", name, heldBy(name, ap))
		}
	}
	// A leftover signpost (a crashed session's, or a re-run) may carry a parking
	// receipt: restore it FIRST, then re-detect. One restore path — the takeover
	// below re-parks with a fresh receipt; no label merging across sessions.
	if err := restoreContainerParked(name); err != nil {
		answer("error: restoring what the previous %s session parked: %v", name, err)
	}
	owners := nameOwners(name, nets)
	receipt := make([]string, 0, len(owners))
	for _, o := range owners {
		receipt = append(receipt, o.id)
	}
	endpoints := map[string]any{}
	for _, n := range nets {
		endpoints[n] = map[string]any{"Aliases": []string{name}}
	}
	body := map[string]any{
		"Image":      self.image,
		"Entrypoint": signpostArgs(pairs, self.relayTarget()),
		"Labels": map[string]string{
			"plug.signpost":       "1",
			"plug.signpost.owner": self.owner(),
			parkedContainersLabel: strings.Join(receipt, ","),
		},
		// Restart it if it ever dies: the Swarm signpost has RestartPolicy any
		// and a k8s pod is restarted by its Deployment — a standalone container
		// had nothing, so one crash took the cluster name down for the rest of
		// the session. `unless-stopped` so the session teardown's stop is final.
		"HostConfig": map[string]any{
			"NetworkMode":   nets[0],
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
		},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{nets[0]: endpoints[nets[0]]}},
	}
	var created struct {
		Id string `json:"Id"`
	}
	if _, err := dockerAPI("POST", "/containers/create?name="+signpostName(name), body, &created); err != nil {
		answer("error: creating the %s signpost: %v", name, err)
	}
	// The alias must exist on EVERY network the agent is on (workloads may look
	// from any of them).
	for _, n := range nets[1:] {
		if _, err := dockerAPI("POST", "/networks/"+n+"/connect",
			map[string]any{"Container": created.Id, "EndpointConfig": endpoints[n]}, nil); err != nil {
			_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
			answer("error: attaching the %s signpost to %s: %v", name, n, err)
		}
	}
	if _, err := dockerAPI("POST", "/containers/"+created.Id+"/start", nil, nil); err != nil {
		_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
		answer("error: starting the %s signpost: %v", name, err)
	}
	// Park AFTER the signpost is live: a brief both-in-DNS overlap is benign
	// round-robin, whereas a no-record gap would leak the lookup to the upstream
	// resolver (bench-proven on Swarm's embedded DNS).
	for i, o := range owners {
		if _, err := dockerAPI("POST", "/containers/"+o.id+"/stop?t=10", nil, nil); err != nil {
			for _, r := range owners[:i] { // roll the partial park back
				_, _ = dockerAPI("POST", "/containers/"+r.id+"/start", nil, nil)
			}
			_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
			answer("error: parking %q (stopping %s): %v", name, o.name, err)
		}
	}
	if len(owners) > 0 {
		answer("dynamic parked")
	}
	answer("dynamic")
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
func agentPortLive(port string) bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// signpostAgentPort digs an agent-side port out of an existing signpost's
// command — [bin, "signpost", p1, t1, p2, t2, …]. The FIRST pair's is enough:
// all of a signpost's ports belong to one session, so one live port means the
// session is alive. "" when the shape is not a signpost's.
func signpostAgentPort(cmd []string) string {
	if len(cmd) < 4 || cmd[1] != "signpost" {
		return ""
	}
	if i := strings.LastIndex(cmd[3], ":"); i > 0 {
		return cmd[3][i+1:]
	}
	return ""
}

// k8sTargetPort reads the targetPort out of a plug Service's ports.
func k8sTargetPort(raw json.RawMessage) string {
	var ports []struct {
		TargetPort any `json:"targetPort"`
	}
	if json.Unmarshal(raw, &ports) != nil || len(ports) == 0 {
		return ""
	}
	switch v := ports[0].TargetPort.(type) {
	case float64:
		return strconv.Itoa(int(v))
	case string:
		return v
	}
	return ""
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
// exists — and carries the alias there, relaying to the agent's service VIP.
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

func swarmServe(name string, pairs []portPair, self selfInfo) {
	// -s relays to the agent's service VIP, and the session's remote-forward
	// lives on ONE task — so >1 replica makes the VIP miss it intermittently.
	// Refuse loudly rather than ship a silent flaky path.
	if r := serviceReplicas(self.service); r > 1 {
		answer("error: the plug agent has %d replicas — plug -s needs a single replica (scale the plug service to 1)", r)
	}
	if serviceIsGlobal(self.service) {
		answer("error: the plug agent runs in GLOBAL mode — plug -s needs a single replica (deploy it as mode: replicated, replicas: 1)")
	}
	nets := self.overlayNets()
	if len(nets) == 0 {
		// Only ingress/bridge — nothing to publish an alias on.
		answer("error: the agent is on no overlay network — attach it to the overlay your " +
			"services use, otherwise the name cannot resolve for them")
	}
	// Serving is the moment the agent runs code anyway: reap the lingers whose
	// grace has passed, THIS name's included — if ours expired, the GET below
	// sees nothing and a fresh create (fresh VIP) is the honest outcome.
	sweepExpiredServiceLingers()
	// A signpost service already carrying this name may belong to a LIVE
	// session — its relay port still answers on this agent — and then the name
	// is taken; a dead port is a crashed session's leftover, swept below.
	var sp struct {
		ID      string `json:"ID"`
		Version struct {
			Index uint64 `json:"Index"`
		} `json:"Version"`
		Spec struct {
			Labels       map[string]string `json:"Labels"`
			TaskTemplate struct {
				ContainerSpec struct {
					Command []string `json:"Command"`
				} `json:"ContainerSpec"`
			} `json:"TaskTemplate"`
		} `json:"Spec"`
	}
	// Whether we can UPDATE the signpost that is already there instead of
	// replacing it. Swarm gives a service its VIP once, at creation, and that VIP
	// is what every caller resolved and cached — recreating hands out a new one
	// and every cached answer points at an address that no longer exists. The
	// callers recover when their cache expires, which is why this looks like a
	// name that "comes back on its own".
	//
	// It matters far more than one session: re-provisioning after a reconnect
	// goes through here too, so today a laptop waking up is enough to move the
	// VIP. Kubernetes already keeps its ClusterIP across park and restore; this
	// brings Swarm in line.
	reuse := false
	if code, err := dockerAPI("GET", "/services/"+signpostName(name), nil, &sp); err == nil && code == 200 {
		// Same rule as the container shape: a service name is cluster-wide, so
		// another agent's LIVE signpost must not read as our leftover just
		// because its port does not answer in our netns.
		if o := sp.Spec.Labels["plug.signpost.owner"]; o != "" && o != self.owner() && ownerAlive(o, true) {
			answer("error: %q is served here by another plug agent (%s), which is still running — "+
				"two agents on one cluster cannot both own a name. Use a different name, or stop that agent.", name, o)
		}
		if ap := signpostAgentPort(sp.Spec.TaskTemplate.ContainerSpec.Command); ap != "" && agentPortLive(ap) {
			answer("error: %q is already exposed by another live session (%s) — one -s per name at a time", name, heldBy(name, ap))
		}
		// Past those two checks the signpost is ours to take: nobody live is
		// behind it. Reuse it UNLESS it carries a parking receipt — that receipt
		// is what scales a real workload back up, and deleting the signpost is
		// how the restore is driven (see restoreServiceParked). Keeping the VIP
		// is not worth risking a deployed service left at zero replicas.
		reuse = sp.ID != "" && sp.Spec.Labels[parkedServiceLabel] == ""
	}
	// A leftover signpost service (a crashed session's, or a re-run) may carry a
	// parking receipt: restore it FIRST, then re-detect — one restore path, and
	// the takeover below re-parks with a fresh receipt. Skipped when we are
	// reusing: there is no receipt to act on (that is what made it reusable),
	// and this is the call that would delete the service and take the VIP with it.
	if !reuse {
		if err := restoreServiceParked(name); err != nil {
			answer("error: restoring what the previous %s session parked: %v", name, err)
		}
	}
	// A real service with this name (anywhere in the cluster) must keep it: the
	// container-scan nameOwners can't see Swarm services, so check them explicitly.
	own := swarmNameOwner(name, self)
	if own != nil {
		if own.global {
			answer("error: %q runs in GLOBAL mode — plug cannot park it (no replica count to restore). Remove it instead: docker service rm %s.", own.name, own.name)
		}
		// A Swarm STACK names its services <stack>_<svc> and carries the short
		// name as a network alias — parking that is exactly the use case (same
		// logical service, stack-prefixed). Refuse only a foreign alias: a
		// service whose own name is unrelated would lose it as collateral.
		if own.viaAlias && !strings.HasSuffix(own.name, "_"+name) {
			answer("error: %q is a network ALIAS of service %q — parking that service would take its own name down too. Remove the alias instead.", name, own.name)
		}
	}

	var attach []map[string]any
	for _, n := range nets {
		attach = append(attach, map[string]any{"Target": n, "Aliases": []string{name}})
	}
	labels := map[string]string{
		"plug.signpost":       "1",
		"plug.signpost.owner": self.owner(),
	}
	if own != nil { // the parking receipt — how unserve/gc restore it
		labels[parkedServiceLabel] = own.name
		labels[parkedReplicasLabel] = strconv.Itoa(max(own.replicas, 1))
	}
	spec := map[string]any{
		"Name":   signpostName(name),
		"Labels": labels,
		"TaskTemplate": map[string]any{
			"ContainerSpec": map[string]any{
				"Image":   pinnedImage(self.image),
				"Command": signpostArgs(pairs, self.relayTarget()),
			},
			"Networks":      attach,
			"RestartPolicy": map[string]any{"Condition": "any"},
		},
		"Mode": map[string]any{"Replicated": map[string]any{"Replicas": 1}},
		// stop-first, explicitly. A signpost relays to ONE agent port, so two
		// tasks behind the VIP is not a smoother rollout — it is half the
		// connections landing on the previous task, which relays to a port that
		// no longer answers. A brief gap is the honest shape here.
		"UpdateConfig": map[string]any{"Order": "stop-first"},
	}
	if reuse {
		// The VIP is kept by updating in place. Swarm requires the version we
		// read the service at, which is also the concurrency guard: if anything
		// touched it since, this fails rather than clobbering.
		path := fmt.Sprintf("/services/%s/update?version=%d", sp.ID, sp.Version.Index)
		if _, err := dockerAPI("POST", path, spec, nil); err != nil {
			answer("error: updating the %s signpost service: %v", name, err)
		}
	} else if _, err := dockerAPI("POST", "/services/create", spec, nil); err != nil {
		answer("error: creating the %s signpost service: %v", name, err)
	}
	if own != nil {
		// Park AFTER the signpost exists: a brief both-in-DNS overlap is benign
		// round-robin, whereas a no-record gap forwards the lookup to the upstream
		// resolver (bench-proven on the embedded DNS).
		if err := scaleService(own.id, 0); err != nil {
			_, _ = dockerAPI("DELETE", "/services/"+signpostName(name), nil, nil)
			answer("error: parking %q (scaling %s to 0): %v", name, own.name, err)
		}
		answer("dynamic parked")
	}
	answer("dynamic")
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
	f := `{"label":["plug.signpost=1"]}`
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

func dockerUnserve(name string) {
	// Drop whichever shape exists — restoring anything its receipt parked FIRST
	// (scale-back / restart, then delete: the name resolves throughout). The
	// container shape is always meaningful; the service shape only on a Swarm
	// manager (off one, /services/* answers 503 — not a real failure). A real
	// failure on either shape is surfaced (a swallowed error would leak the
	// signpost or leave the parked service down).
	if err := restoreContainerParked(name); err != nil {
		answer("error: removing the %s signpost: %v", name, err)
	}
	if swarmManager() {
		if err := restoreServiceParked(name); err != nil {
			answer("error: removing the %s signpost service: %v", name, err)
		}
	}
	answer("ok")
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

func dockerGC() {
	self, err := dockerSelf()
	if err != nil {
		gcNote("cannot identify this agent (%v) — leftovers from crashed sessions were NOT swept", err)
		return
	}
	mine := self.owner()
	swarm := swarmManager()
	f := `{"label":["plug.signpost=1"]}`
	// Standalone-container signposts.
	var clist []struct {
		Id     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if _, err := dockerAPI("GET", "/containers/json?all=1&filters="+urlEscape(f), nil, &clist); err == nil {
		for _, c := range clist {
			o := c.Labels["plug.signpost.owner"]
			if o == mine || !ownerAlive(o, swarm) {
				// An orphaned signpost's receipt is a takeover that never got
				// restored (the session died with the agent) — restore it now,
				// then sweep the signpost.
				if failed := restartParkedContainers(c.Labels[parkedContainersLabel]); len(failed) > 0 {
					gcNote("could not restart %s while cleaning up %s — start them by hand",
						strings.Join(failed, ", "), c.Labels[parkedContainersLabel])
				}
				_, _ = dockerAPI("DELETE", "/containers/"+c.Id+"?force=1", nil, nil)
			}
		}
	}
	// Swarm-service signposts (only reachable on a manager).
	if !swarm {
		return
	}
	var slist []struct {
		ID   string `json:"ID"`
		Spec struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services?filters="+urlEscape(f), nil, &slist); err == nil {
		now := time.Now()
		for _, s := range slist {
			// The linger rule comes FIRST, before ownership: a lingering
			// signpost is holding an ADDRESS warm, and an agent restart renames
			// the owner — judged by the owner rule alone it would read as an
			// orphan and be swept, killing the very address the linger exists to
			// keep. Within the grace it stays, whoever stamped it; past the
			// grace it goes, whoever stamped it.
			if stamp := s.Spec.Labels[lingerLabel]; stamp != "" {
				if !lingerExpired(stamp, now) {
					continue
				}
				_, _ = dockerAPI("DELETE", "/services/"+s.ID, nil, nil)
				continue
			}
			o := s.Spec.Labels["plug.signpost.owner"]
			if o == mine || !ownerAlive(o, swarm) {
				if err := scaleBackParkedService(s.Spec.Labels); err != nil { // undo the orphan's takeover
					gcNote("could not scale %q back up while cleaning up: %v",
						s.Spec.Labels[parkedServiceLabel], err)
				}
				_, _ = dockerAPI("DELETE", "/services/"+s.ID, nil, nil)
			}
		}
	}
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

// containerExists reports whether a container named `name` is present (running
// or not).
func containerExists(name string) bool {
	code, err := dockerAPI("GET", "/containers/"+name+"/json", nil, nil)
	return err == nil || code != 404
}

// serviceExists reports whether a Swarm service named `name` is present.
func serviceExists(name string) bool {
	code, err := dockerAPI("GET", "/services/"+name, nil, nil)
	return err == nil || code != 404
}

// serviceReplicas returns the replica count of a Swarm service (1 if it can't
// tell, or for a global/unset mode — don't block on uncertainty).
func serviceReplicas(name string) int {
	var s struct {
		Spec struct {
			Mode struct {
				Replicated struct {
					Replicas int `json:"Replicas"`
				} `json:"Replicated"`
			} `json:"Mode"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+name, nil, &s); err != nil {
		return 1
	}
	if s.Spec.Mode.Replicated.Replicas == 0 {
		return 1
	}
	return s.Spec.Mode.Replicated.Replicas
}

// serviceIsGlobal reports whether a Swarm service runs in GLOBAL mode (one task
// per node). serviceReplicas can't see that (global has no Replicated block, so
// it reads as 1), yet the VIP then spreads across nodes and the session's single
// remote-forward task is missed intermittently — so -s must refuse it too.
func serviceIsGlobal(name string) bool {
	var s struct {
		Spec struct {
			Mode struct {
				Global *struct{} `json:"Global"`
			} `json:"Mode"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+name, nil, &s); err != nil {
		return false
	}
	return s.Spec.Mode.Global != nil
}

func urlEscape(s string) string {
	return strings.NewReplacer("{", "%7B", "}", "%7D", `"`, "%22", "[", "%5B", "]", "%5D", ",", "%2C", ":", "%3A", "=", "%3D").Replace(s)
}

// ---- kubernetes backend (RBAC applied — part of deploy/plug-k8s.yaml) ----

const k8sSA = "/var/run/secrets/kubernetes.io/serviceaccount"

func k8sAvailable() bool {
	_, err := os.Stat(k8sSA + "/token")
	return err == nil
}

func k8sAPI(method, path string, body any, out any) (int, error) {
	return k8sDo(method, path, "application/json", body, out)
}

// k8sMergePatch applies an RFC 7386 JSON merge patch — how the takeover
// repoints a real Service's selector (and how the restore puts it back): object
// keys merge (null deletes a key), arrays replace whole.
func k8sMergePatch(path string, body any) (int, error) {
	return k8sDo("PATCH", path, "application/merge-patch+json", body, nil)
}

func k8sDo(method, path, contentType string, body any, out any) (int, error) {
	token, err := os.ReadFile(k8sSA + "/token")
	if err != nil {
		return 0, err
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(k8sSA + "/ca.crt"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "https://kubernetes.default.svc"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		_ = json.Unmarshal(data, out)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("%s", firstNonEmpty(e.Message, resp.Status))
	}
	return resp.StatusCode, nil
}

func k8sNamespace() string {
	ns, _ := os.ReadFile(k8sSA + "/namespace")
	return strings.TrimSpace(string(ns))
}

const k8sManaged = "app.kubernetes.io/managed-by"

// k8sParkedAnn is the k8s parking receipt: the takeover repoints the REAL
// Service at the agent and stores its original selector+ports here, on the
// object itself — so the restore (unserve or boot gc) survives any agent crash.
// The annotation is written ONLY when absent: a crashed takeover session leaves
// the Service already pointing at plug, and re-saving would overwrite the
// original with {app: plug}, losing the way back.
const k8sParkedAnn = "plug.softwarity.io/parked"

// k8sReceipt is what the annotation stores — everything the restore re-patches.
type k8sReceipt struct {
	Selector map[string]string `json:"selector"`
	Ports    json.RawMessage   `json:"ports"`
}

// k8sRepointPatch builds the merge patch that points an EXISTING Service at the
// agent — the ONE place both branches (a real workload taken over, and a plug
// Service reclaimed from a linger or a crash) build it, because they had drifted
// apart and only one of them was right.
//
// The reclaim branch wrote a bare {app: plug}. Under RFC 7386 a map MERGES, so
// on any Service whose selector carried more than that key, `app: plug` was
// ADDED to the original keys instead of replacing them — and a selector demanding
// app=plug AND app.kubernetes.io/name=<the workload> matches no pod at all. The
// Service ends up with zero endpoints and the name times out, which is the exact
// failure selectorPatch exists to prevent.
func k8sRepointPatch(pairs []portPair, current map[string]string, ann map[string]any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"annotations": ann},
		"spec": map[string]any{
			"selector": selectorPatch(map[string]string{"app": "plug"}, current),
			"ports":    k8sPorts(pairs),
		},
	}
}

// k8sUnroutable explains why an EXISTING Service cannot carry a plug name, or
// returns "" when it can. Two shapes cannot, and NEITHER fails at patch time:
//
//   - headless (clusterIP: None): there is no virtual IP, so the name resolves
//     straight to pod IPs and targetPort is never applied. A caller would reach
//     the agent pod on the CLUSTER port — where nothing listens, the session's
//     forward sitting on a port sshd allocated.
//   - type: ExternalName: a DNS alias elsewhere, carrying no endpoints and no
//     ports at all.
//
// Both patch cleanly and then time out, which is exactly how one real session
// spent 90s and 95 attempts blaming the cluster's scheduler. clusterIP is
// immutable, so there is no in-place fix to suggest: the Service goes, or the
// name does.
func k8sUnroutable(name, typ, clusterIP string) string {
	switch {
	case clusterIP == "None":
		return fmt.Sprintf("the Service %q is headless (clusterIP: None) — the name resolves straight to pod IPs "+
			"and targetPort is never applied, so plug cannot route a session through it. Delete it and let plug "+
			"create the name (kubectl delete service %s), or serve a different name", name, name)
	case typ == "ExternalName":
		return fmt.Sprintf("the Service %q is a type: ExternalName — a DNS alias carrying no endpoints and no ports, "+
			"so plug cannot route a session through it. Delete it and let plug create the name "+
			"(kubectl delete service %s), or serve a different name", name, name)
	}
	return ""
}

// k8sPorts renders the pairs as a Service's ports. The per-port name is
// REQUIRED by k8s as soon as there is more than one — a multi-port service
// (HTTP+SMTP+POP3 on one name) is exactly the case this serves.
func k8sPorts(pairs []portPair) []map[string]any {
	out := make([]map[string]any, 0, len(pairs))
	for _, pp := range pairs {
		c, _ := strconv.Atoi(pp.cluster)
		a, _ := strconv.Atoi(pp.agent)
		out = append(out, map[string]any{"name": "p" + pp.cluster, "port": c, "targetPort": a})
	}
	return out
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
			_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+s.Metadata.Name, nil, nil)
		}
	}
}

func k8sServe(name string, pairs []portPair) {
	ns := k8sNamespace()
	sweepExpiredK8sLingers(ns)
	svc := map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": name, "labels": map[string]string{k8sManaged: "plug"}},
		"spec": map[string]any{
			// The official manifest labels the agent `app: plug` — the Service
			// (the k8s "signpost") points the name at it, each cluster port on
			// the sshd-allocated port that session's forward listens on.
			"selector": map[string]string{"app": "plug"},
			"ports":    k8sPorts(pairs),
		},
	}
	code, err := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil)
	switch {
	case err == nil:
		answer("dynamic")
	case code == 403:
		// No RBAC → the opt-in was never applied, so it cannot create the name.
		answer("error: the agent may not manage Services in namespace %s — apply the RBAC (deploy/plug-k8s.yaml)", ns)
	case code == 409:
		// The name exists. A previous plug session's leftover is replaced; a REAL
		// Service keeps its name — unless takeover, which repoints it at the agent
		// for the session (selector+ports), receipt in an annotation on itself.
		var existing struct {
			Metadata struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Selector  map[string]string `json:"selector"`
				Ports     json.RawMessage   `json:"ports"`
				Type      string            `json:"type"`
				ClusterIP string            `json:"clusterIP"`
			} `json:"spec"`
		}
		_, gerr := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing)
		// A Service plug cannot route THROUGH is refused here, in the millisecond
		// the agent already holds the object — not 90 seconds later as a timeout
		// the caller has to interpret. Repointing one SUCCEEDS (selector and ports
		// patch cleanly) and yields a name that answers nobody: the worst of both,
		// since the deployed workload is parked and the replacement never carries
		// traffic. Checked before the ownership split, so it covers a takeover and
		// a stale plug Service alike.
		if gerr == nil {
			if why := k8sUnroutable(name, existing.Spec.Type, existing.Spec.ClusterIP); why != "" {
				answer("error: %s", why)
			}
		}
		if gerr != nil || existing.Metadata.Labels[k8sManaged] != "plug" {
			if gerr == nil {
				receipt := existing.Metadata.Annotations[k8sParkedAnn]
				if receipt == "" { // first takeover of this Service — save the way back
					b, merr := json.Marshal(k8sReceipt{Selector: existing.Spec.Selector, Ports: existing.Spec.Ports})
					if merr != nil {
						answer("error: recording %q's original spec: %v", name, merr)
					}
					receipt = string(b)
				}
				patch := k8sRepointPatch(pairs, existing.Spec.Selector, map[string]any{k8sParkedAnn: receipt})
				if _, perr := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); perr != nil {
					answer("error: parking the Service %q (repointing it at the agent): %v", name, perr)
				}
				answer("dynamic parked")
			}
			answer("error: the Service %q exists but plug cannot read it — remove it, or grant the agent access: kubectl delete service %s", name, name)
		}
		// It's ours — but "ours" may be ANOTHER LIVE SESSION's. The sshd bind
		// used to be the collision check (one fixed port per name); with
		// allocated ports, ask the port itself: if the existing Service's
		// targetPort still answers on this agent, its session is alive and the
		// name is taken. A dead port is a crashed session's leftover — replaced.
		if tp := k8sTargetPort(existing.Spec.Ports); tp != "" && agentPortLive(tp) {
			answer("error: %q is already exposed by another live session (%s) — one -s per name at a time", name, heldBy(name, tp))
		}
		// Take it over IN PLACE — never delete-and-recreate. The ClusterIP is
		// handed out at creation and it is what every caller cached: patching
		// ports and selector (and clearing any linger stamp) keeps it, whether
		// this Service was left lingering by a clean unserve or orphaned by a
		// crash. Only if the patch itself fails do we fall back to the old
		// replace, reporting the real cause.
		patch := k8sRepointPatch(pairs, existing.Spec.Selector, map[string]any{lingerLabel: nil})
		if _, perr := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); perr != nil {
			_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil)
			if _, rerr := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil); rerr != nil {
				answer("error: re-provisioning the Service %q failed (a stale plug Service was removed): %v", name, rerr)
			}
		}
		answer("dynamic")
	default:
		answer("error: %v", err)
	}
}

// k8sRestoreParked undoes a takeover on one Service: re-patch its original
// selector+ports from the receipt annotation, and drop the annotation. Reports
// whether the Service was parked at all.
func k8sRestoreParked(ns, name string, ann map[string]string, current map[string]string) (bool, error) {
	raw := ann[k8sParkedAnn]
	if raw == "" {
		return false, nil
	}
	var r k8sReceipt
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return true, fmt.Errorf("unreadable parking receipt on %q: %v", name, err)
	}
	patch := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{k8sParkedAnn: nil}},
		"spec": map[string]any{
			"selector": selectorPatch(r.Selector, current),
			"ports":    r.Ports,
		},
	}
	if _, err := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); err != nil {
		return true, err
	}
	return true, nil
}

func k8sUnserve(name string) {
	ns := k8sNamespace()
	var existing struct {
		Metadata struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing); err != nil {
		// ONLY an absent Service means "nothing to drop". Any other failure —
		// 500, timeout, RBAC revoked mid-session — must not read as success: a
		// Service this session PARKED (selector repointed at the agent) would
		// stay repointed at a dead forward while the CLI reports the name
		// released, and nothing would restore it until the agent's boot gc.
		if code == 404 {
			answer("ok")
		}
		answer("error: reading the Service %q to release it: %v — anything this session parked is still parked", name, err)
	}
	if existing.Metadata.Labels[k8sManaged] == "plug" {
		// Ours: the plug-created Service LINGERS instead of dying — its
		// ClusterIP is what every caller cached (600s TTL, Docker and CoreDNS
		// alike), and a relaunch takes it over in place. See the linger block
		// by restoreServiceParked. Reaped by gc/serve once the grace passes.
		patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{lingerLabel: lingerStamp()}}}
		if _, err := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); err != nil {
			answer("error: %v", err)
		}
		answer("ok")
	}
	// A REAL Service we parked (takeover): restore it from its receipt.
	if _, err := k8sRestoreParked(ns, name, existing.Metadata.Annotations, existing.Spec.Selector); err != nil {
		answer("error: restoring the Service %q: %v", name, err)
	}
	answer("ok") // restored, or never ours to begin with
}

func k8sGC() {
	ns := k8sNamespace()
	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Selector map[string]string `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	// One un-filtered list serves both sweeps: parked REAL Services (annotation —
	// restore them) and plug-created ones (label — delete them).
	if _, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services", nil, &list); err != nil {
		gcNote("cannot list Services in %s (%v) — leftovers from crashed sessions were NOT swept", ns, err)
		return
	}
	now := time.Now()
	for _, s := range list.Items {
		if s.Metadata.Labels[k8sManaged] == "plug" {
			// Same first rule as the Swarm gc: a lingering Service within its
			// grace keeps its ClusterIP warm across even an agent restart —
			// sweeping it would kill the address the linger exists to keep.
			if stamp := s.Metadata.Annotations[lingerLabel]; stamp != "" && !lingerExpired(stamp, now) {
				continue
			}
			_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+s.Metadata.Name, nil, nil)
			continue
		}
		_, _ = k8sRestoreParked(ns, s.Metadata.Name, s.Metadata.Annotations, s.Spec.Selector)
	}
}
