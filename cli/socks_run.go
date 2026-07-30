package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// dialTunnel opens the SSH transport to the cluster agent. Shared by coreRun
// (Linux/Windows) and the macOS datapath daemon. coreRun itself is per-OS
// (socks_run_darwin.go / socks_run_other.go): macOS routes through a persistent
// daemon, elsewhere each launch is autonomous.
func dialTunnel(cfg config) (*tunnel.Transport, error) {
	knownHosts := ""
	// TOFU host-key pinning is pointless for a loopback agent (there is no network
	// to intercept) and just causes false "host key changed" errors when a local
	// dev agent is recreated with a fresh key — so skip it for localhost. For real
	// hosts, pin the key next to the profiles.
	if !isLoopback(cfg.host) {
		if shared := tun.SharedKnownHosts(); shared != "" {
			// Windows: a machine-wide, user-writable path (%ProgramData%\plug) shared by
			// the SYSTEM service and the launcher. The service can't pin under the user's
			// home, and its own profile dir isn't user-accessible — so a "host key changed"
			// there could not be reset without admin. Here the user can remove the line.
			knownHosts = shared
		} else if home, err := os.UserHomeDir(); err == nil {
			knownHosts = filepath.Join(home, ".plug", "known_hosts")
		}
	}
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, knownHosts, info)
	// The host key is pinned into knownHosts on first connect. Off localhost the
	// dialer may be the setuid daemon (euid 0), which would leave the pin file
	// root-owned under the user's ~/.plug — and the "key changed, remove the line"
	// hint would then point at a file they can't edit without sudo. Hand it back.
	if err == nil && knownHosts != "" {
		chownToUser(knownHosts)
	}
	return tr, err
}

// How long startExposes insists on a name before calling it unreachable.
//
// The agent answers as soon as the signpost is CREATED, not once it carries
// traffic — and the gap is not free to observe. A Swarm VIP whose task isn't
// running yet, like a k8s Service with no endpoint, DROPS the SYN instead of
// refusing it: an early probe can't fail fast, it hangs for its whole budget.
//
// Both numbers come from measuring the same cluster. Provisioning is wildly
// variable — the same signpost service reached Running in 3s, 8s, 10s, 45s and
// 56s on one Docker Desktop Swarm — so the two failure modes need separating:
//
//   - Probe LENGTH decides what a too-early probe costs. Spending the whole
//     budget in one 10s block bought a single answer, and made even a 3s
//     provisioning wait 11.5s to be noticed. Short probes, close together,
//     catch the path within a beat of it opening.
//   - Probe BUDGET decides what counts as never. It has to clear the slow end
//     of that spread, or a session fails on a name that was merely still
//     coming up — the old 44.5s total sat right in the middle of it, which is
//     exactly when a wait turns into an intermittent error.
//
// The per-probe budget grows toward the ceiling: a name still silent after
// several seconds is less and less likely to be a provisioning race and more
// likely to be genuinely slow, and the late probes must be generous enough not
// to convict a slow link.
//
// Var, not const, so the retry test doesn't have to spend the real budget.
var (
	exposeVerifyBudget = 90 * time.Second       // overall deadline, all attempts
	exposeVerifyFirst  = 750 * time.Millisecond // first attempt's dial budget
	exposeVerifyMax    = 8 * time.Second        // ceiling it doubles up to
	exposeVerifyGap    = 250 * time.Millisecond // pause between attempts
	exposeVerifyNotify = 5 * time.Second        // say we're waiting past this…
	exposeVerifyRenote = 15 * time.Second       // …and again this often
	// The one synchronous probe. Sized to outlast a REJECTION (the cluster
	// answering "that name resolves nowhere" — 75ms measured, and it is the
	// answer worth blocking for), not to outlast provisioning.
	exposeVerifyUpfront = 1 * time.Second
)

// pathVerifier is what verifyExposed needs of an armed mapping — *tunnel.Exposed
// in production, a counter in the test.
type pathVerifier interface {
	Verify(time.Duration) error
}

// errStopped ends a background check because the session is going away — not a
// verdict on the path, and nothing to report.
var errStopped = errors.New("session ending")

// verifyExposed proves the mapping's path end to end, retrying while the name
// the agent just created is still being scheduled by the cluster.
//
// stopped is polled between probes: this runs alongside the session, and a check
// outliving it would keep narrating a name nobody is waiting on any more.
func verifyExposed(ex pathVerifier, name string, stopped func() bool) error {
	start := time.Now()
	deadline := start.Add(exposeVerifyBudget)
	budget := exposeVerifyFirst
	// Waiting on someone else's scheduler can take a minute. Silence for that
	// long reads as a hang, so say what is being waited on — and keep saying it,
	// or the first line looks like the last thing that happened before a freeze.
	nextNote := start.Add(exposeVerifyNotify)
	for attempt := 1; ; attempt++ {
		if stopped() {
			return errStopped
		}
		left := time.Until(deadline)
		if left <= 0 {
			// Never return nil for "ran out of time" — the caller turns a nil
			// into a verified session.
			return fmt.Errorf("gave up after %s of retries", exposeVerifyBudget)
		}
		if now := time.Now(); now.After(nextNote) {
			info("%s: waiting for the cluster name to come up (%s so far) — the agent has provisioned it, "+
				"the cluster is still scheduling it", name, now.Sub(start).Round(time.Second))
			nextNote = now.Add(exposeVerifyRenote)
		}
		err := ex.Verify(min(budget, left))
		if err == nil {
			return nil
		}
		if time.Until(deadline) <= 0 {
			// Say we insisted: a bare "context deadline exceeded" reads as one
			// unlucky probe, and sends the reader looking for a hiccup rather
			// than for a name that never came up.
			return fmt.Errorf("%w\n      (still failing after %s and %d attempts — the name never carried traffic)",
				err, exposeVerifyBudget, attempt)
		}
		budget = min(budget*2, exposeVerifyMax)
		time.Sleep(exposeVerifyGap)
	}
}

// startExposes arms the session's -s mappings (the reverse direction) on a
// DEDICATED transport, so the listeners' lifetime is exactly this session's —
// even on macOS/Windows where the forward datapath lives in a shared daemon.
//
// Everything that can be decided AT ONCE is decided here, and fails the session:
// an agent that cannot provision names at all, a port another instance already
// exposes, a name the agent refuses, a workload it cannot park. Each mapping is
// then proven end-to-end (through the cluster's own DNS) in the BACKGROUND — a
// too-old agent image or a competing instance still gets said out loud, but the
// wait for a cluster to schedule the name is not charged to the command the
// user is launching.
// Returns the transport teardown (a no-op when nothing is exposed).
func startExposes(cfg config) (func(), error) {
	if len(cfg.exposes) == 0 {
		return func() {}, nil
	}
	tr, err := dialTunnel(cfg)
	if err != nil {
		return nil, err
	}
	// Set by teardown, read by the background path checks: once the transport is
	// closing, their failures are the session ending, not a verdict on the path.
	var done atomic.Bool
	// Names provisioned so far — dropped on teardown AND on any error below (a
	// signpost/Service created for spec N must not survive a failure on N or N+1).
	var dynamic []string
	drop := func() {
		for _, name := range dynamic {
			_, _ = tr.Exec("unserve-name " + name)
		}
	}
	fail := func(err error) (func(), error) {
		drop()
		tr.Close()
		return nil, err
	}
	// The serve-name verb: name, the port workloads dial, the sshd-ALLOCATED
	// port the signpost must relay to (see tunnel/expose.go — allocation is
	// what lets many names share one cluster port), takeover (parking a
	// deployed workload owning the name is the DEFAULT — restored on exit).
	verb := func(spec tunnel.ExposeSpec, agentPort string) string {
		return "serve-name " + spec.Name + " " + spec.ClusterPort + " " + agentPort + " takeover"
	}
	// After a reconnect, a restarted agent has GC'd the signpost — AND, on a
	// takeover, restored the parked workload — so re-run the SAME verb (re-park
	// included) and re-verify: the name must not be silently dead (or silently
	// back on the deployed version) while the forward reports re-armed.
	armRearm := func(ex *tunnel.Exposed, spec tunnel.ExposeSpec) {
		ex.OnRearm(func(agentPort string) error {
			m, err := tr.Exec(verb(spec, agentPort))
			if err != nil {
				return err
			}
			if strings.HasPrefix(m, "error:") {
				return fmt.Errorf("agent: %s", strings.TrimSpace(strings.TrimPrefix(m, "error:")))
			}
			// Same race as at startup, and worse to get wrong here: the verb
			// above just re-created the signpost, so a single early probe would
			// time out and report the name dead while it was merely coming up —
			// a scary note about a path that then works.
			return verifyExposed(ex, spec.Name, done.Load)
		})
	}
	for _, spec := range cfg.exposes {
		ex, err := tr.Expose(spec)
		if err != nil {
			return fail(err)
		}
		// Ask the agent to provision the NAME (a docker signpost, a Swarm
		// service, a k8s Service — whatever the deployment has). Provisioning is
		// the whole point of -s: you name a service and it exists, with nothing
		// to agree cluster-side beforehand.
		reply, err := tr.Exec(verb(spec, ex.AgentPort()))
		if err != nil {
			return fail(err)
		}
		if strings.HasPrefix(reply, "error:") {
			msg := strings.TrimSpace(strings.TrimPrefix(reply, "error:"))
			return fail(fmt.Errorf("%s: agent: %s", spec.Name, msg))
		}
		// "dynamic" may carry the "parked" note: a deployed workload was parked
		// (stopped / scaled to 0 / repointed) and will be restored on teardown.
		fields := strings.Fields(reply)
		// An agent that created the name answers "dynamic". Anything else is off
		// protocol — an agent that failed says so above, with the access it is
		// missing, so there is nothing to add here beyond refusing to continue.
		if len(fields) == 0 || fields[0] != "dynamic" {
			return fail(fmt.Errorf("%s: agent answered %q, expected \"dynamic\"", spec.Name, strings.TrimSpace(reply)))
		}
		parked := len(fields) > 1 && fields[1] == "parked"
		// Provisioned — register for cleanup BEFORE the check, so a failure
		// below still tears the name down.
		dynamic = append(dynamic, spec.Name)
		if parked {
			info("took over %s — the deployed workload is parked for this session (restored on exit)", spec.Name)
		}
		// -s was asked for explicitly: an unproven path must never pass silently
		// (fix the cluster side, run again). But proving it is NOT always the
		// user's wait to bear. Measured on one Docker Desktop Swarm, the phases
		// of this loop are: dial 0.04s, remote bind 0.00s, serve-name 0.03s —
		// and then 6s, 37s, 29s on three identical runs, all of it Swarm
		// scheduling the signpost task. That wait belongs to the cluster, not to
		// the command the user is launching.
		//
		// The name was created a moment ago, so a probe that fails now means
		// "not scheduled yet" far more often than anything else — and the two
		// are indistinguishable from the error alone: a freshly created Swarm
		// service has no VIP yet, so the cluster answers "Name does not resolve"
		// (75ms) word for word as it would for a name that will never exist.
		// Branching on that failed every session at startup (one regression's
		// worth of learning). So: one short probe, because a cluster that is
		// already ready answers in 1-5ms and the session is then proven before
		// it starts — and otherwise the wait moves off the critical path.
		if verr := ex.Verify(exposeVerifyUpfront); verr == nil {
			info("serving %s (path verified through the cluster)", spec)
			armRearm(ex, spec)
			continue
		}
		info("serving %s (proving the path in the background)", spec)
		go func() {
			verr := verifyExposed(ex, spec.Name, done.Load)
			// Teardown closes the transport, which fails an in-flight probe.
			// That is the session ending, not a broken path: don't diagnose it.
			if errors.Is(verr, errStopped) || done.Load() {
				return
			}
			if verr == nil {
				info("%s: path verified through the cluster", spec.Name)
				return
			}
			info("WARNING %v\n"+
				"      %s is armed but nothing ever reached it. The session keeps running — a name that "+
				"comes up later still works — but as it stands the cluster cannot see this process.\n"+
				"      Check it cluster-side: docker service ps plug-sp-%s / kubectl get svc %s",
				verr, spec.Name, spec.Name, spec.Name)
		}()
		armRearm(ex, spec)
	}
	return func() {
		done.Store(true)
		// Drop the dynamic names BEFORE the transport goes: a signpost/Service
		// must not outlive its session.
		drop()
		tr.Close()
	}, nil
}

// isLoopback reports whether host is the local machine (no network to intercept).
func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
