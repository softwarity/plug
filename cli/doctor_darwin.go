//go:build darwin

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
)

// doctorOS — the macOS-side checks: the setuid grant, the per-cluster daemon
// (alive? which CORE binary does it actually run — the one thing the
// per-cluster version mechanism does not refresh), the system resolver state
// (a stale plug override with no session is THE dirty state), Docker Desktop's
// belt-and-braces DNS, and a live honest-NXDOMAIN probe through a running
// datapath.
func doctorOS(add func(check)) {
	// Privilege: the INSTALLED launcher is setuid root (granted once at
	// install). Check that one — running doctor from a dev build must not
	// cry wolf about the build's own file mode.
	target, _ := os.Executable()
	if home, err := os.UserHomeDir(); err == nil {
		if inst := home + "/.local/bin/plug"; fileExists(inst) {
			target = inst
		}
	}
	if fi, err := os.Stat(target); err == nil {
		if fi.Mode()&os.ModeSetuid != 0 {
			add(check{area: "local", name: "privilege", status: stOK,
				detail: "setuid grant in place (" + target + ")"})
		} else {
			add(check{area: "local", name: "privilege", status: stWarn,
				detail: target + " is not setuid root",
				remedy: "re-run the install one-liner (it grants the privilege once)"})
		}
	}

	// Live daemons, found by PROCESS (the flock in /var/run/plug is root-owned
	// — probing it as a user reads "no daemon" and once turned a perfectly
	// healthy teardown window into a "stale resolver" verdict). Each line shows
	// WHICH core binary the daemon actually runs — the version gap made visible.
	daemons := 0
	if out, err := exec.Command(tun.HelperPath("ps"), "-axo", "pid=,command=").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, "__plug-daemon") {
				continue
			}
			daemons++
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			pid, bin := f[0], f[1]
			// `plug down` belongs HERE and nowhere else: on the line that says
			// something is running, as a statement of fact about how to stop it —
			// never as a remedy. Printed as a remedy it reads as "do this to fix
			// your problem", which it almost never is: closing the sessions lets
			// the daemon stop by itself 30s later, and that is the answer to every
			// version question. Fifteen fruitless invocations came out of that
			// confusion.
			detail := "running (pid " + pid + ", plug down stops it)"
			if v := versionFromCorePath(bin); v != "" {
				detail += ", core v" + v
			} else {
				detail += ", " + bin
			}
			add(check{area: "local", name: "daemon", status: stOK, detail: detail})
		}
	}

	// Alive is not the same as working: probe the datapath's own resolver. A
	// second probe before condemning it — the first can time out on a loaded
	// machine, and --fix acts on this verdict.
	if daemons > 0 {
		ok := tun.DatapathResponsive(2 * time.Second)
		if !ok {
			ok = tun.DatapathResponsive(2 * time.Second)
		}
		stopped := false
		if !ok && doctorFix {
			stopped = stopDaemon()
		}
		if c, show := datapathVerdict(true, ok, stopped); show {
			add(c)
		}
	}

	// System resolver: pointed at plug? Legitimate while sessions live; STALE
	// (the daemon crashed / teardown missed) when nothing runs — the state that
	// broke machine-wide DNS once.
	out, _ := exec.Command(tun.HelperPath("scutil"), "--dns").Output()
	plugged := strings.Contains(string(out), "198.18.")
	sessions := 0
	for _, k := range tun.ActiveClusters() {
		sessions += tun.LiveClients(k)
	}
	switch {
	case plugged && sessions == 0 && daemons == 0:
		// THE dirty state: a daemon died without tidying up, so the machine's
		// resolver points at an address nothing answers on — nothing resolves,
		// system-wide. It is also the one thing here that repairs itself with no
		// judgement call, which is why --fix does it rather than printing a
		// remedy. `plug down` reaches the same code, but naming it here taught
		// everyone to use a teardown command as a repair tool.
		if doctorFix {
			tun.RestoreOrphanDNS(globalKey)
			add(check{area: "local", name: "system resolver", status: stOK,
				detail: "was left pointed at plug by a daemon that died — resolver restored"})
		} else {
			add(check{area: "local", name: "system resolver", status: stFail,
				detail: "still pointed at plug with NO live daemon and no session (stale override)",
				remedy: "plug doctor --fix (restores the resolver)"})
		}
	case plugged && sessions == 0:
		// The daemon lives, the last client just left: the self-teardown window,
		// a legitimate in-between (it restores the resolver on its way out).
		add(check{area: "local", name: "system resolver", status: stOK,
			detail: "plugged, daemon alive, no client — teardown pending (normal)"})
	case plugged:
		add(check{area: "local", name: "system resolver", status: stOK,
			detail: fmt.Sprintf("plugged (%d live client(s)) — normal while sessions run", sessions)})
	default:
		add(check{area: "local", name: "system resolver", status: stOK, detail: "untouched"})
	}

	// WHERE dotted names actually go. Read from what the running datapath
	// published, never re-derived from the system here: those two answers differ
	// exactly when it matters — a capture that went stale (the VPN moved after
	// the session started) looks perfectly healthy if you ask the system again.
	// This is the one place that can show the difference.
	if ups := tun.CurrentUpstreams(); len(ups) > 0 {
		d := "forwarding dotted names to " + strings.Join(ups, ", ")
		st := stOK
		for _, u := range ups {
			if strings.HasPrefix(u, "8.8.8.8") || strings.HasPrefix(u, "1.1.1.1") {
				// The announced fallback: it keeps public names working, but
				// internal ones will not resolve and those lookups leave the
				// network. Worth flagging, not failing.
				st = stWarn
				d += " — a PUBLIC resolver: internal names will not resolve, and these lookups leave your network"
			}
		}
		add(check{area: "local", name: "dns forwarding", status: st, detail: d})
	}

	// Docker Desktop on this machine: with explicit upstream DNS, unknown names
	// never even leave the VM (belt and braces on top of honest NXDOMAIN).
	if home, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(home + "/.docker/daemon.json"); err == nil {
			if strings.Contains(string(data), `"dns"`) {
				add(check{area: "local", name: "docker desktop dns", status: stOK,
					detail: `daemon.json pins upstream "dns" (belt and braces)`})
			} else {
				add(check{area: "local", name: "docker desktop dns", status: stOK,
					detail: `no "dns" pin — the agent's lookups for names it does not know are forwarded out of the VM, ` +
						`i.e. back to this Mac; pinning one keeps them inside (see the NXDOMAIN check below)`})
			}
		}
	}

	// Honest-NXDOMAIN, live: with a datapath running, an absent bare name must
	// FAIL to resolve. A minted 198.18 answer says the name was minted without
	// being checked — but NOT why, and the two whys need opposite remedies.
	if plugged && sessions > 0 {
		start := time.Now()
		addrs, err := net.LookupHost("plug-doctor-absent-probe")
		addr := ""
		if len(addrs) > 0 {
			addr = addrs[0]
		}
		add(nxdomainVerdict(addr, err, time.Since(start), sessions))
	}
}

// probeStall is the point past which a probe was not answered but ABANDONED.
// The CLI gives the agent 3s to say whether a name exists and mints if the
// answer does not come (transport.go, "a wedged session must not stall DNS").
// Anything close to that is the timeout, not a verdict — and a verdict that
// arrives in milliseconds is a different story entirely.
const probeStall = 2 * time.Second

// nxdomainVerdict turns ONE probe measurement into a check. Split out from the
// probe itself so the reasoning is testable without a datapath, a cluster or a
// network — and because it got this wrong once, in the way that costs most: it
// named a single cause ("the datapath predates 2.2") with a remedy that could
// not work, for a symptom every Docker Desktop user reproduces.
//
// What the duration tells us, and the earlier version ignored:
//
//   - fast + minted  → the check ran and said "exists", or did not run at all.
//     On a current datapath that means the agent answered something other than
//     nxdomain. On an old one, that there is no check. Version tells them apart,
//     and doctor prints both versions two lines above.
//   - slow + minted  → the check RAN and timed out. plug minted rather than
//     lie. On Docker Desktop the cause is almost always the loop: the agent's
//     own lookup is forwarded upstream, upstream is this Mac, and this Mac is
//     plugged — the question comes back to the stub that asked it.
func nxdomainVerdict(addr string, err error, took time.Duration, sessions int) check {
	const name = "honest NXDOMAIN"
	minted := err == nil && strings.HasPrefix(addr, "198.18.")
	switch {
	case minted && took >= probeStall:
		return check{area: "local", name: name, status: stWarn,
			detail: fmt.Sprintf("an absent name was minted after %s — the existence check timed out, "+
				"so plug minted rather than answer for a cluster it could not ask", took.Round(100*time.Millisecond)),
			remedy: `Docker Desktop is sending the agent's lookups back to this Mac. Settings → Docker Engine, add "dns": ["1.1.1.1"], apply & restart`}
	case minted:
		// The datapath predates the check, or never got an answer. Either way the
		// fix is the same and it is NOT `plug down`: the daemon stops by itself
		// once nothing has used it for 30s, and the next launch starts one on the
		// current core. Say how many sessions stand in the way — the reason this
		// looks like it "does nothing" is always that one is still open.
		return check{area: "local", name: name, status: stWarn,
			detail: "an absent name minted a fake IP immediately — the running datapath did not check whether it exists",
			remedy: fmt.Sprintf("close ALL your plug sessions (%d still open) and wait ~30s — the daemon stops on its own, "+
				"and the next launch picks up the core the agent now serves (compare `cached cores` with the agent version above)", sessions)}
	case err != nil && took >= probeStall:
		// No address, but far too slow to call it a clean NXDOMAIN: something on
		// the path is timing out. Saying OK here would hide exactly the problem
		// this check exists to surface.
		return check{area: "local", name: name, status: stWarn,
			detail: fmt.Sprintf("an absent name failed to resolve, but took %s to do so — something on the DNS path is timing out",
				took.Round(100*time.Millisecond)),
			remedy: `if this machine runs Docker Desktop: Settings → Docker Engine, add "dns": ["1.1.1.1"], apply & restart`}
	case err != nil:
		return check{area: "local", name: name, status: stOK,
			detail: "absent names answer NXDOMAIN through the live datapath"}
	default:
		// Resolved to something that is not ours: a real name, or a resolver
		// answering wildcards. Either way this probe proves nothing here.
		return check{area: "local", name: name, status: stOK,
			detail: "not conclusive here — the probe name resolved to " + addr + ", which is not plug's doing"}
	}
}

// doctorSessions reports the registry view: which clusters have live clients.
func doctorSessions(add func(check)) {
	keys := tun.ActiveClusters()
	if len(keys) == 0 {
		add(check{area: "local", name: "sessions", status: stOK, detail: "none running"})
		return
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s (%d)", k, tun.LiveClients(k)))
	}
	add(check{area: "local", name: "sessions", status: stOK, detail: strings.Join(parts, " · ")})
}

// versionFromCorePath extracts "x.y.z" from a cached-core path
// (…/.plug/versions/<v>/plug), or "" when the path is not one.
func versionFromCorePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if s == "versions" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// resolverRestartRemedy: on macOS the system resolver is mDNSResponder, and the
// state it gets into is cleared by a flush plus a HUP — the same pair plug does
// internally when it re-asserts its own override.
func resolverRestartRemedy() string {
	return "sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder"
}
