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
// knownHostsFor is where this agent's host key is recorded, "" when it should not
// be recorded at all.
//
// TOFU pinning is pointless for a loopback agent (there is no network to
// intercept) and just causes false "host key changed" errors when a local dev
// agent is recreated with a fresh key, so it is skipped for localhost. For real
// hosts, the key is pinned next to the profiles.
//
// Shared by the tunnel and the download channel, so one agent is recorded once.
func knownHostsFor(host string) string {
	if isLoopback(host) {
		return ""
	}
	if shared := tun.SharedKnownHosts(); shared != "" {
		// Windows: a machine-wide, user-writable path (%ProgramData%\plug) shared by
		// the SYSTEM service and the launcher. The service can't pin under the user's
		// home, and its own profile dir isn't user-accessible, so a "host key changed"
		// there could not be reset without admin. Here the user can remove the line.
		return shared
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".plug", "known_hosts")
	}
	return ""
}

func dialTunnel(cfg config) (*tunnel.Transport, error) {
	knownHosts := knownHostsFor(cfg.host)
	// The pin file is written by the tunnel package, possibly with euid 0 — same
	// rule as every other write under the user's home (see guardUserPath).
	if knownHosts != "" {
		guardUserPath(knownHosts)
	}
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, cfg.authKeys(), knownHosts, info)
	// The host key is pinned into knownHosts on first connect. Off localhost the
	// dialer may be the setuid daemon (euid 0), which would leave the pin file
	// root-owned under the user's ~/.plug — and the "key changed, remove the line"
	// hint would then point at a file they can't edit without sudo. Hand it back.
	if err == nil && knownHosts != "" {
		chownToUser(knownHosts)
	}
	return tr, explainRefusal(cfg, err)
}

// explainRefusal turns "key SHA256:… is not authorized" into something the
// person can act on.
//
// The agent names the fingerprint it refused, and that is all it can do: it has
// no idea where the key came from. The CLIENT knows, and knowing is the whole
// difference. Someone who ran `plug keygen`, enrolled what `plug pubkey`
// printed, and was then refused by an unfamiliar fingerprint has no way to tell
// that the key they enrolled was never offered at all. Pairing each fingerprint
// with the file it came from says it in one line.
func explainRefusal(cfg config, err error) error {
	var af *tunnel.AuthFailure
	if !errors.As(err, &af) {
		return err
	}
	names := cfg.authKeyNames()
	var b strings.Builder
	fmt.Fprintf(&b, "the agent at %s refused every key plug offered:", af.Addr)
	for i, fp := range af.Offered {
		src := "an unnamed key"
		if i < len(names) {
			src = names[i]
		}
		fmt.Fprintf(&b, "\n        %s  %s", fp, src)
	}
	if cfg.key == "" {
		b.WriteString("\n      this profile has no key of its own, so only the shared one was offered." +
			"\n      If the cluster enrols developer keys: 'plug keygen', then hand what" +
			"\n      'plug pubkey' prints to whoever operates it")
	} else {
		b.WriteString("\n      what to enrol is exactly what 'plug pubkey' prints for this profile")
	}
	return errors.New(b.String())
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
	// How long a re-provisioner waits for the rest of a name's mappings to
	// re-arm before rebuilding the signpost. Re-arms of one wave land within
	// milliseconds of each other (one listenRemote each, on the connection the
	// first one restored); this only has to outlast that.
	rearmSettle = 300 * time.Millisecond
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
// gaveUp says the two things a reader needs: that it kept trying, and what kept
// failing. The second half used to depend on WHERE the loop ran out of time. The
// path after a failed attempt wrapped that attempt's error; the path at the top
// of the next iteration returned "gave up after N of retries" and threw away the
// error it was already holding, leaving a message that says it insisted without
// saying at what. Which path fires is a matter of milliseconds - coverage
// instrumentation on a Windows runner was enough to move it - so a reader got a
// different quality of answer depending on the weather.
//
// No attempt completing at all is the one case with genuinely nothing to name,
// and it says so rather than implying a cause it does not have.
func gaveUp(last error, attempts int) error {
	if last == nil {
		return fmt.Errorf("gave up after %s of retries, with no attempt completing", exposeVerifyBudget)
	}
	// A bare "context deadline exceeded" reads as one unlucky probe, and sends
	// the reader looking for a hiccup rather than for a name that never came up.
	return fmt.Errorf("%w\n      (still failing after %s and %d attempts — the name never carried traffic)",
		last, exposeVerifyBudget, attempts)
}

func verifyExposed(ex pathVerifier, name string, stopped func() bool) error {
	start := time.Now()
	deadline := start.Add(exposeVerifyBudget)
	budget := exposeVerifyFirst
	// Waiting on someone else's scheduler can take a minute. Silence for that
	// long reads as a hang, so say what is being waited on — and keep saying it,
	// or the first line looks like the last thing that happened before a freeze.
	nextNote := start.Add(exposeVerifyNotify)
	var last error // the most recent failure, so giving up can still name a cause
	for attempt := 1; ; attempt++ {
		if stopped() {
			return errStopped
		}
		left := time.Until(deadline)
		if left <= 0 {
			// Never return nil for "ran out of time" — the caller turns a nil
			// into a verified session.
			return gaveUp(last, attempt-1)
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
		last = err
		if time.Until(deadline) <= 0 {
			return gaveUp(last, attempt)
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
	// The mappings of each name, by name. Declared here — before drop — because
	// the teardown has to tell the agent WHICH session is releasing the name,
	// and that identity is the current agent port of the name's first mapping.
	groups := map[string][]*tunnel.Exposed{}
	// Forgets this session's ~/.plug/served records — the breadcrumb that lets a
	// LATER session name whoever holds a name it is refused.
	var unmark []func()
	drop := func() {
		for _, f := range unmark {
			f()
		}
		for _, name := range dynamic {
			// Say it when this fails. Releasing a name is not just removing a
			// signpost: on a takeover it RESTORES what the session parked
			// (containers restarted, service scaled back, k8s Service
			// repointed). The likeliest moment for the Exec to fail is a
			// network already gone — which is exactly when you Ctrl-C — and
			// leaving silently would let you walk away believing a workload
			// came back up when it is still down.
			// Name the port we hold it on. If this name went to another session
			// while we were away (a sleep past the keepalive frees it — the
			// lease only holds while the port answers), the agent must NOT act
			// on our teardown, or we would delete the signpost our successor is
			// now serving and restore a workload it had parked.
			mine := ""
			if g := groups[name]; len(g) > 0 {
				mine = " " + g[0].AgentPort()
			}
			out, err := tr.Exec("unserve-name " + name + mine)
			switch {
			case out == "ok reassigned":
				info("%s went to another session while this one was running — left alone, "+
					"it belongs to that session now.", name)
			case err != nil:
				info("WARNING could not release %s (%v) — anything this session parked is STILL parked.\n"+
					"      Re-run the session to restore it, or restart the agent (its boot gc restores).", name, err)
			case strings.HasPrefix(out, "error:"):
				info("WARNING releasing %s: %s — anything this session parked is STILL parked.",
					name, strings.TrimSpace(strings.TrimPrefix(out, "error:")))
			}
		}
	}
	fail := func(err error) (func(), error) {
		drop()
		tr.Close()
		return nil, err
	}
	// The serve-name verb: ONE per NAME, all its cluster ports at once — a
	// service exposing HTTP+SMTP+POP3 on one name is ONE signpost listening on
	// all three, each relayed to that mapping's sshd-ALLOCATED port (see
	// tunnel/expose.go — allocation is what lets many names share one cluster
	// port). takeover: parking a deployed workload owning the name is the
	// DEFAULT — restored on exit. The pairs read the groupmates' CURRENT
	// AgentPort at call time, so a re-arm that re-allocated one forward's port
	// re-provisions the signpost with every port fresh.
	verb := func(group []*tunnel.Exposed) string {
		pairs := make([]string, 0, len(group))
		for _, g := range group {
			pairs = append(pairs, g.Spec().ClusterPort+":"+g.AgentPort())
		}
		return "serve-name " + group[0].Spec().Name + " " + strings.Join(pairs, ",") + " takeover"
	}
	// After a reconnect, a restarted agent has GC'd the signpost — AND, on a
	// takeover, restored the parked workload — so re-run the SAME verb (re-park
	// included) and re-verify: the name must not be silently dead (or silently
	// back on the deployed version) while the forward reports re-armed.
	//
	// ONE re-provisioner per NAME, fed by every member's hook. A transport death
	// re-arms all of a name's mappings independently, each on a freshly
	// allocated port: re-provisioning per member would read the others' ports
	// while they are still being reallocated, and rebuild the signpost once per
	// member — on Swarm, ~8.5s of delete+recreate each time. So the hooks only
	// signal, and this goroutine coalesces the wave: it waits for it to settle,
	// then sends ONE serve-name carrying every member's current port.
	armRearm := func(group []*tunnel.Exposed) {
		name := group[0].Spec().Name
		trigger := make(chan struct{}, 1)
		for _, ex := range group {
			ex.OnRearm(func() {
				select {
				case trigger <- struct{}{}:
				default: // a wave is already pending; it will read our port too
				}
			})
		}
		go func() {
			for range trigger {
				// Let the rest of the wave land, then drain what it queued: the
				// verb below reads every member's port at call time, so one pass
				// covers them all.
				time.Sleep(rearmSettle)
				select {
				case <-trigger:
				default:
				}
				if done.Load() {
					return
				}
				m, err := tr.Exec(verb(group))
				switch {
				case err != nil:
					info("WARNING %s: re-provisioning after reconnect failed (%v) — the name may be unreachable", name, err)
					continue
				case strings.HasPrefix(m, "error:"):
					info("WARNING %s: agent refused to re-provision after reconnect: %s", name, strings.TrimSpace(strings.TrimPrefix(m, "error:")))
					continue
				}
				// Same race as at startup, and worse to get wrong here: the verb
				// above just re-created the signpost, so a single early probe
				// would time out and report the name dead while it was merely
				// coming up — a scary note about a path that then works.
				if verr := verifyExposed(group[0], name, done.Load); verr != nil {
					if !errors.Is(verr, errStopped) && !done.Load() {
						info("WARNING %s: re-provisioned after reconnect but nothing reached it (%v)", name, verr)
					}
					continue
				}
				info("%s: re-provisioned and verified after reconnect", name)
			}
		}()
	}
	// Arm every forward first — each -s gets its own sshd-allocated port — and
	// group the mappings by NAME: one signpost carries a name, so a name's
	// ports must reach the agent in one verb (a second one would read as a
	// second SESSION on the name and bounce on the liveness check).
	var order []string
	for _, spec := range cfg.exposes {
		ex, err := tr.Expose(spec)
		if err != nil {
			return fail(err)
		}
		if _, seen := groups[spec.Name]; !seen {
			order = append(order, spec.Name)
		}
		groups[spec.Name] = append(groups[spec.Name], ex)
	}
	for _, name := range order {
		group := groups[name]
		// Ask the agent to provision the NAME (a docker signpost, a Swarm
		// service, a k8s Service — whatever the deployment has). Provisioning is
		// the whole point of -s: you name a service and it exists, with nothing
		// to agree cluster-side beforehand.
		reply, err := tr.Exec(verb(group))
		// Refused because a live session holds the name? If that session is one
		// of OURS — same agent port, so it is the holder and not a leftover
		// naming a recycled PID — offer to stop it. A terminal is required to
		// ask: no prompt in a script or a CI job.
		if err == nil && strings.Contains(reply, "another live session") {
			if h := servedHolder(name); holderIsOurs(h, reply) && askToStop(h) {
				if serr := stopHolder(h); serr != nil {
					return fail(fmt.Errorf("%s: could not stop the session holding it: %w", name, serr))
				}
				info("%s released — taking it", name)
				reply, err = tr.Exec(verb(group))
			}
		}
		if err != nil {
			return fail(err)
		}
		if strings.HasPrefix(reply, "error:") {
			msg := strings.TrimSpace(strings.TrimPrefix(reply, "error:"))
			// "already exposed by another live session" is correct and unhelpful
			// on its own: the holder is a process you may have no window onto
			// (an editor closed, its terminal panes gone, what ran in them still
			// running). If it is on this machine, we know which one.
			if strings.Contains(msg, "another live session") {
				if h := servedHolder(name); h != nil {
					return fail(fmt.Errorf("%s: agent: %s\n      held on this machine by %s\n"+
						"      Check it is yours, then free the name with:  kill %d", name, msg, h.describe(), h.pid))
				}
				return fail(fmt.Errorf("%s: agent: %s\n"+
					"      No session of yours on this machine is recorded for it — the holder is on\n"+
					"      another machine or another account. It frees itself once that session ends.", name, msg))
			}
			return fail(fmt.Errorf("%s: agent: %s", name, msg))
		}
		// "dynamic" may carry the "parked" note: a deployed workload was parked
		// (stopped / scaled to 0 / repointed) and will be restored on teardown.
		fields := strings.Fields(reply)
		// An agent that created the name answers "dynamic". Anything else is off
		// protocol — an agent that failed says so above, with the access it is
		// missing, so there is nothing to add here beyond refusing to continue.
		if len(fields) == 0 || fields[0] != "dynamic" {
			return fail(fmt.Errorf("%s: agent answered %q, expected \"dynamic\"", name, strings.TrimSpace(reply)))
		}
		parked := len(fields) > 1 && fields[1] == "parked"
		// Provisioned — register for cleanup BEFORE the check, so a failure
		// below still tears the name down.
		dynamic = append(dynamic, name)
		unmark = append(unmark, markServed(name, group[0].AgentPort(), os.Args[1:]))
		if parked {
			info("took over %s — the deployed workload is parked for this session (restored on exit)", name)
		}
		for _, ex := range group {
			spec := ex.Spec()
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
		}
		// One re-provisioner for the whole name, once its mappings are armed.
		armRearm(group)
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

// runCoreInProcess is the autonomous datapath: this process dials its own tunnel
// and holds it for the child's lifetime.
//
// It existed twice, as coreRun on Linux and coreRunInProcess on Windows, the same
// twenty lines under two names. Windows chooses between the service model and
// this one at runtime, Linux only ever has this one, and neither difference is in
// the body. Having it once means a fix to the teardown order, or to what is said
// when the tunnel will not open, lands on both rather than on whichever file the
// person had open.
func runCoreInProcess(cfg config, cmdArgs []string) int {
	tr, err := dialTunnel(cfg)
	if err != nil {
		info("connect: %v", err)
		return 1
	}
	defer tr.Close()

	stopExposes, err := startExposes(cfg)
	if err != nil {
		info("expose: %v", err)
		return 1
	}
	defer stopExposes()

	info("tunnel ready - running your command")
	code, rerr := tun.Run(tr, cmdArgs, info)
	if rerr != nil {
		info("%v", rerr)
	}
	return code
}
