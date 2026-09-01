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
	return readAPIReply(resp.StatusCode, resp.Status, data, out, true)
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
		if s.Spec.Labels[signpostLabel] == "1" {
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
		if o := insp.Config.Labels[signpostOwnerLabel]; o != "" && o != self.owner() && ownerAlive(o, false) {
			answer("error: %q is served here by another plug agent (%s), which is still running — "+
				"two agents on one host cannot both own a name. Use a different name, or stop that agent.", name, o)
		}
		// Ask the session's own address, not 127.0.0.1: past that owner check the
		// signpost may still belong to a SIBLING replica of this same deployment
		// (same role, same owner label), whose forward answers on the network and
		// never in this container's loopback.
		if own := signpostOwner(insp.Config.Labels, insp.Config.Entrypoint); sessionLive(own) {
			answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
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
		"Image":      signpostImage(self.image),
		"Entrypoint": signpostArgs(pairs, self.relayTarget()),
		"Labels": map[string]string{
			signpostLabel:         "1",
			signpostOwnerLabel:    self.owner(),
			sessionOwnerLabel:     sessionOwner(self.relayTarget(), pairs),
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

func swarmServe(name string, pairs []portPair, self selfInfo) {
	// Two refusals used to stand here: more than one replica, and global mode.
	// Both existed because the signpost relayed to the service VIP, which load
	// balances across every task while a session's forward lives on exactly one -
	// so -s was a lottery past a single task, and global mode hid it (one task per
	// node, and no replica count that would have shown it). relayTarget names the
	// TASK now, so neither refusal has anything left to protect, and the counting
	// they needed went with them. What survives of the question is per-NAME rather
	// than per-agent, and is answered below where it belongs: a name a live
	// session already holds is refused, whichever task that session landed on.
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
		if o := sp.Spec.Labels[signpostOwnerLabel]; o != "" && o != self.owner() && ownerAlive(o, true) {
			answer("error: %q is served here by another plug agent (%s), which is still running — "+
				"two agents on one cluster cannot both own a name. Use a different name, or stop that agent.", name, o)
		}
		// Dial the session's own address: a sibling TASK of this same service
		// shares our owner label, so the check above waves it through, and its
		// forward answers on the overlay and never in this task's loopback.
		if own := signpostOwner(sp.Spec.Labels, sp.Spec.TaskTemplate.ContainerSpec.Command); sessionLive(own) {
			answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
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
		signpostLabel:      "1",
		signpostOwnerLabel: self.owner(),
		sessionOwnerLabel:  sessionOwner(self.relayTarget(), pairs),
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
	f := `{"label":["` + signpostLabel + `=1"]}`
	// Standalone-container signposts.
	var clist []struct {
		Id     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if _, err := dockerAPI("GET", "/containers/json?all=1&filters="+urlEscape(f), nil, &clist); err == nil {
		for _, c := range clist {
			// Before ownership: is anyone still USING it? The owner label names a
			// role, so every replica of one deployment reads its siblings' live
			// signposts as "mine" and would sweep them at boot - restoring what
			// their sessions parked while those sessions are still serving it.
			if sessionLive(c.Labels[sessionOwnerLabel]) {
				continue
			}
			o := c.Labels[signpostOwnerLabel]
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
			// A sibling task's session, same as the container shape above: its
			// forward answers on the overlay, and the owner label cannot tell it
			// apart from ours because both tasks report the same service.
			if sessionLive(s.Spec.Labels[sessionOwnerLabel]) {
				continue
			}
			o := s.Spec.Labels[signpostOwnerLabel]
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
	return readAPIReply(resp.StatusCode, resp.Status, data, out, false)
}

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

// k8sSignReceipt records WHO is parking, without disturbing WHAT was parked. A
// second session taking the same Service over inherits the duty of restoring it,
// so the owner is refreshed while the saved selector and ports stay exactly as
// they are: only the FIRST takeover ever saw the original, and re-saving would
// hand the way back to a Service that already points at an agent.
func k8sSignReceipt(raw, owner string, fresh k8sReceipt) (string, error) {
	r := fresh
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			// Unreadable: leave the only way back untouched rather than rewrite
			// it. The restore says the same thing when it gets there.
			return raw, nil
		}
	}
	r.Owner = owner
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// k8sReceiptOwner is the owner an annotation names, "" when there is no receipt,
// no owner, or nothing readable.
func k8sReceiptOwner(raw string) string {
	var r k8sReceipt
	if raw == "" || json.Unmarshal([]byte(raw), &r) != nil {
		return ""
	}
	return r.Owner
}

// k8sRepointPatch builds the merge patch that points an EXISTING Service at the
// agent — the ONE place both branches (a real workload taken over, and a plug
// Service reclaimed from a linger or a crash) build it, because they had drifted
// apart and only one of them was right.
//
// `selector: null` drops the selector outright, and that is the point: a selector
// names a ROLE (`app: plug` matches every replica of the agent) while the session
// serving the name lives in exactly ONE pod. At more than one replica the name
// then works for one request in N, which is not a slow name but a lottery. Without
// a selector the endpoints controller keeps its hands off and the Endpoints are
// ours to write (k8sPointAtSelf), naming the POD that holds the session.
//
// It also retires a whole class of failure this patch had to defend against. The
// reclaim branch used to write a bare {app: plug}, and since RFC 7386 MERGES maps,
// on a Service whose selector carried more than that key the result demanded
// app=plug AND app.kubernetes.io/name=<the workload>: no pod at all, zero
// endpoints, and a name that times out. Deleting the key cannot half-merge.
func k8sRepointPatch(pairs []portPair, ann map[string]any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"annotations": ann},
		"spec": map[string]any{
			"selector": nil,
			"ports":    k8sPorts(pairs),
		},
	}
}

// k8sSelectorFallback puts the OLD shape back: a Service selecting the agent by
// label. It is what a cluster whose deployed RBAC predates the endpoints grant
// gets, and it is right there: that RBAC was written for the single replica the
// manifest deploys, where `app: plug` and "this pod" are the same pod.
//
// Answering an error instead would break a working deployment on nothing but an
// image upgrade: `plug update` moves the image and never the manifest, so an
// agent that self-updated would stop serving names until someone re-applied a
// YAML file. The gap is named at boot instead (k8sNoteEndpointsGrant), where a
// line on stderr is the container's log rather than the one line of protocol a
// verb is allowed to print.
func k8sSelectorFallback(ns, name string) error {
	patch := map[string]any{"spec": map[string]any{"selector": map[string]string{"app": "plug"}}}
	_, err := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch)
	return err
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
		out = append(out, map[string]any{"name": k8sPortName(pp), "port": c, "targetPort": a})
	}
	return out
}

// k8sPortName is that name, in one place because the Endpoints must repeat it
// verbatim: k8s matches a Service port to an endpoint port BY NAME, and a
// mismatch is a name with no route rather than a rejected object.
func k8sPortName(pp portPair) string { return "p" + pp.cluster }

// k8sSelfIP is THIS pod's address: what a served name must resolve to, and half
// of the identity its parking receipt is signed with.
//
// The manifest supplies it through the downward API, which is the only source
// that is certain. The fallback matters as much, because an agent linked into a
// gateway is deployed by whoever wrote THAT manifest and cannot demand a line in
// it: the source address the kernel would pick to reach the API server is this
// pod's address on the cluster network. `net.Dial` on UDP only binds a socket
// and sends nothing, and it needs no RBAC. KUBERNETES_SERVICE_HOST is an address
// the kubelet always sets, so the fallback does not depend on DNS either.
func k8sSelfIP() string {
	if ip := os.Getenv("PLUG_POD_IP"); ip != "" {
		return ip
	}
	api := firstNonEmpty(os.Getenv("KUBERNETES_SERVICE_HOST"), "kubernetes.default.svc")
	c, err := net.Dial("udp", net.JoinHostPort(api, "443"))
	if err != nil {
		return ""
	}
	defer c.Close()
	host, _, err := net.SplitHostPort(c.LocalAddr().String())
	if err != nil {
		return ""
	}
	return host
}

// k8sServiceFor is the Service a served name IS: no selector, and an annotation
// naming the pod that holds the session.
//
// A selector would name a role. `app: plug` matches every replica of the agent,
// while the forward carrying the name lives in exactly one pod, so the name would
// work for one request in N - not degraded, drawn by lot. What replaces it is
// k8sEndpointsFor, which names an address.
func k8sServiceFor(name, owner string, pairs []portPair) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":   name,
			"labels": map[string]string{k8sManaged: "plug"},
			// The label says a plug Service; this says WHOSE, so a sibling
			// replica's boot gc can tell a live session's name from a crashed
			// session's leftover instead of sweeping both alike.
			"annotations": map[string]string{sessionOwnerLabel: owner},
		},
		"spec": map[string]any{"ports": k8sPorts(pairs)},
	}
}

// k8sEndpointsFor is what makes a selector-less Service reach exactly this pod:
// one address, and one port per exposure carrying the port the session's forward
// actually listens on. targetPort is not consulted for a Service without a
// selector, so the endpoint's own port is the whole routing decision.
//
// Deliberately unlabelled. The Service says whose the name is; an Endpoints
// object that outlives a takeover is simply adopted by the endpoints controller
// the moment the selector comes back, and a plug label on it would be one more
// thing to clean up for no one to read.
func k8sEndpointsFor(name, podIP string, pairs []portPair) map[string]any {
	ports := make([]map[string]any, 0, len(pairs))
	for _, pp := range pairs {
		a, _ := strconv.Atoi(pp.agent)
		ports = append(ports, map[string]any{"name": k8sPortName(pp), "port": a})
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Endpoints",
		"metadata":   map[string]any{"name": name},
		"subsets": []map[string]any{{
			"addresses": []map[string]any{{"ip": podIP}},
			"ports":     ports,
		}},
	}
}

// k8sWriteEndpoints creates them, or replaces whatever is there: a previous
// session's, or the ones the endpoints controller wrote while the Service still
// had a selector. PUT rather than PATCH so no subset of a stale object survives.
func k8sWriteEndpoints(ns, name, podIP string, pairs []portPair) (int, error) {
	ep := k8sEndpointsFor(name, podIP, pairs)
	code, err := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/endpoints", ep, nil)
	if code == 409 {
		return k8sAPI("PUT", "/api/v1/namespaces/"+ns+"/endpoints/"+name, ep, nil)
	}
	return code, err
}

// k8sPointAtSelf finishes a serve: the name now exists, and this is what makes
// it reach THIS pod. Only two things send it to the old selector shape - no
// address to publish, and no grant to publish it with - and both are a
// deployment that predates this, never a cluster refusing the write. A real
// failure stops the serve, because a name that resolves to nothing is the one
// outcome worse than no name.
func k8sPointAtSelf(ns, name, podIP string, pairs []portPair) {
	if podIP != "" {
		code, err := k8sWriteEndpoints(ns, name, podIP, pairs)
		switch endpointsOutcome(code, err) {
		case endpointsDone:
			return
		case endpointsFatal:
			answer("error: pointing %q at this agent (writing its endpoints): %v", name, err)
		}
	}
	if err := k8sSelectorFallback(ns, name); err != nil {
		answer("error: pointing %q at this agent: %v", name, err)
	}
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

// k8sDropName removes a plug-created name whole. The endpoints controller sweeps
// an Endpoints object whose Service is gone, but only once it notices; deleting
// both here means the name never survives its Service even for a moment. Safe
// because it is only ever called for a Service labelled plug-managed, whose
// Endpoints of the same name are this agent's own.
func k8sDropName(ns, name string) {
	_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil)
	_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/endpoints/"+name, nil, nil)
}

// k8sNoteEndpointsGrant says, once per boot and in the container's log, that this
// agent will fall back to the old selector shape. A verb cannot say it: its
// stdout and stderr are merged into the one line the CLI reads as the answer, so
// a warning there would BE the answer. Boot is the other moment the agent runs
// code, and it is where the person who applied the manifest is looking.
func k8sNoteEndpointsGrant(ns string) {
	if k8sSelfIP() == "" {
		gcNote("this pod's address is unknown, so a served name will select every agent replica by " +
			"label instead of naming this pod - right at one replica, a lottery past it. Re-apply " +
			"deploy/plug-k8s.yaml, or set PLUG_POD_IP from the downward API (status.podIP)")
		return
	}
	// A GET on a name nothing uses separates "may not touch endpoints" (403) from
	// "there is none" (404): one call, and no object created to find out.
	if code, _ := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/endpoints/plug-endpoints-grant-probe", nil, nil); code == 403 {
		gcNote("the deployed RBAC predates the endpoints grant, so a served name will select every agent " +
			"replica by label instead of naming this pod - right at one replica, a lottery past it. " +
			"Re-apply deploy/plug-k8s.yaml")
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

func k8sServe(name string, pairs []portPair) {
	ns := k8sNamespace()
	sweepExpiredK8sLingers(ns)
	podIP := k8sSelfIP()
	owner := sessionOwner(podIP, pairs)
	svc := k8sServiceFor(name, owner, pairs)
	code, err := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil)
	switch {
	case err == nil:
		k8sPointAtSelf(ns, name, podIP, pairs)
		answer("dynamic")
	case code == 403:
		// No RBAC → the opt-in was never applied, so it cannot create the name.
		answer("error: the agent may not manage Services in namespace %s — apply the RBAC (deploy/plug-k8s.yaml)", ns)
	case code == 409:
		// The name exists. A previous plug session's leftover is replaced; a REAL
		// Service keeps its name — unless takeover, which repoints it at the agent
		// for the session (its endpoints and ports), receipt in an annotation on
		// itself.
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
				// A REAL Service, and it may already be parked by a session that
				// is still serving it from another pod: taking it over now would
				// leave that session's name pointing here, and the workload parked
				// twice over. The receipt says whose it is; the address says
				// whether that is still true.
				if own := k8sReceiptOwner(existing.Metadata.Annotations[k8sParkedAnn]); own != owner && sessionLive(own) {
					answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
				}
				receipt, rerr := k8sSignReceipt(existing.Metadata.Annotations[k8sParkedAnn], owner,
					k8sReceipt{Selector: existing.Spec.Selector, Ports: existing.Spec.Ports})
				if rerr != nil {
					answer("error: recording %q's original spec: %v", name, rerr)
				}
				patch := k8sRepointPatch(pairs, map[string]any{k8sParkedAnn: receipt, sessionOwnerLabel: owner})
				if _, perr := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); perr != nil {
					answer("error: parking the Service %q (repointing it at the agent): %v", name, perr)
				}
				k8sPointAtSelf(ns, name, podIP, pairs)
				answer("dynamic parked")
			}
			answer("error: the Service %q exists but plug cannot read it — remove it, or grant the agent access: kubectl delete service %s", name, name)
		}
		// It's ours, but "ours" may be ANOTHER LIVE SESSION's, and with more than
		// one agent replica that session is not necessarily in this pod. The
		// annotation names the pod holding it and the port its forward answers on:
		// if that address answers, the name is taken. A dead one is a crashed
		// session's leftover, replaced.
		//
		// The port alone was the question while `app: plug` was the answer to
		// every name, and a Service an older agent created still carries nothing
		// else: fall back to dialling it here, which is exactly as far as that
		// Service's own single replica could see.
		if own := existing.Metadata.Annotations[sessionOwnerLabel]; own != "" {
			if sessionLive(own) {
				answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
			}
		} else if tp := k8sTargetPort(existing.Spec.Ports); tp != "" && agentPortLive(tp) {
			answer(nameHeldRefusal, name, heldBy(name, tp))
		}
		// Take it over IN PLACE — never delete-and-recreate. The ClusterIP is
		// handed out at creation and it is what every caller cached: patching
		// ports and endpoints (and clearing any linger stamp) keeps it, whether
		// this Service was left lingering by a clean unserve or orphaned by a
		// crash. Only if the patch itself fails do we fall back to the old
		// replace, reporting the real cause.
		patch := k8sRepointPatch(pairs, map[string]any{lingerLabel: nil, sessionOwnerLabel: owner})
		if _, perr := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); perr != nil {
			k8sDropName(ns, name)
			if _, rerr := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil); rerr != nil {
				answer("error: re-provisioning the Service %q failed (a stale plug Service was removed): %v", name, rerr)
			}
		}
		k8sPointAtSelf(ns, name, podIP, pairs)
		answer("dynamic")
	default:
		answer("error: %v", err)
	}
}

// k8sRestoreParked undoes a takeover on one Service: re-patch its original
// selector+ports from the receipt annotation, and drop the annotation. Reports
// whether the Service was parked at all.
//
// The Endpoints this agent wrote are left where they are. Restoring the selector
// hands them straight back to the endpoints controller, which adopts the object
// by name and rewrites it with the workload's own pods; deleting them ourselves
// would only add a gap for the controller to fill.
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
		"metadata": map[string]any{"annotations": map[string]any{k8sParkedAnn: nil, sessionOwnerLabel: nil}},
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
		//
		// Its Endpoints go, and the owner annotation with them: a lingering name
		// belongs to nobody, and a Service with no endpoints REFUSES connections
		// where one still naming a pod that has moved on would swallow them until
		// they time out. That refusal is the behaviour the linger promises, "still
		// resolving, refusing connections like any stopped service".
		patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{
			lingerLabel:       lingerStamp(),
			sessionOwnerLabel: nil,
		}}}
		if _, err := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); err != nil {
			answer("error: %v", err)
		}
		_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/endpoints/"+name, nil, nil)
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
			// The label says "a plug Service", never "MY plug Service". With one
			// agent that was the same sentence; with a replica booting beside
			// three that are serving, it is the difference between sweeping this
			// pod's leftovers and cutting a colleague's session off mid-request.
			if sessionLive(s.Metadata.Annotations[sessionOwnerLabel]) {
				continue
			}
			k8sDropName(ns, s.Metadata.Name)
			continue
		}
		// A parked REAL Service. Restoring it is no longer reserved to the agent
		// that parked it: the receipt names its owner, and an owner that no longer
		// answers is gone for good (scaled down, rescheduled, replaced), so the
		// workload it left parked is anyone's to put back. One that still answers
		// is serving right now, and used to be restored from under itself by any
		// sibling that happened to boot.
		if sessionLive(k8sReceiptOwner(s.Metadata.Annotations[k8sParkedAnn])) {
			continue
		}
		_, _ = k8sRestoreParked(ns, s.Metadata.Name, s.Metadata.Annotations, s.Spec.Selector)
	}
	k8sNoteEndpointsGrant(ns)
}
