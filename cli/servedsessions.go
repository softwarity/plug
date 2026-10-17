package main

// The sessions of this machine that serve a name, in plug doctor: which ones
// are alive, under which profile, and whether each name answers right now.
//
// A session prints what it does and otherwise stays out of the way, which
// leaves two questions while it runs: is my name actually reachable, and what
// is that other session of mine still holding. Both are answered here rather
// than by a command of their own, because doctor is already where someone looks
// when a name does not answer, and plug mcp serves the same checks to an agent.
//
// The source is ~/.plug/served, the records a session writes when it serves a
// name (servedmark.go). servedHolder already reads one safely, as the user's,
// and drops a record whose process is gone, so a crashed session never shows
// up as live.

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

// servedEntry is one live record and the name it serves.
type servedEntry struct {
	name string
	rec  *servedRecord
}

// listServed returns the live sessions of this machine that serve a name,
// sorted by name. A directory that cannot be read is no session at all: this
// feeds a diagnosis and must never be the thing that fails.
func listServed() []servedEntry {
	des, err := os.ReadDir(servedDir())
	if err != nil {
		return nil
	}
	var out []servedEntry
	for _, de := range des {
		if de.IsDir() || !exposeName.MatchString(de.Name()) {
			continue
		}
		if r := servedHolder(de.Name()); r != nil {
			out = append(out, servedEntry{name: de.Name(), rec: r})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// servedBelongsTo reports whether a record goes under profile, whose agent is
// at hostport, among the profiles that exist. The profile the session named
// wins; a session started with --host alone, or from a profile since removed,
// goes under every profile that points at the same agent. A record with no
// cluster at all (an older core wrote it) belongs to none: it is listed with
// the machine's own checks instead.
func servedBelongsTo(r *servedRecord, profile, hostport string, profiles map[string]bool) bool {
	if r.profile != "" && profiles[r.profile] {
		return r.profile == profile
	}
	return r.cluster != "" && r.cluster == hostport
}

// profileAddrs maps every readable profile to its agent's host:port.
func profileAddrs() map[string]string {
	out := map[string]string{}
	for _, p := range listProfiles() {
		if c, err := readProfileSoft(p); err == nil {
			out[p] = net.JoinHostPort(c.host, c.port)
		}
	}
	return out
}

// portProbe is what doctor found for one mapping of a served name: through the
// cluster (the name and port a caller dials, from the agent), and on this
// machine (the port the session relays to).
type portProbe struct {
	port       servedPort
	clusterErr error
	localErr   error
}

// clusterDialer is the one thing the cluster half of the probe needs from the
// tunnel doctor already opened: a dial from the agent's side.
type clusterDialer interface {
	DialClusterTimeout(addr string, d time.Duration) (net.Conn, error)
}

const (
	servedClusterProbe = 5 * time.Second
	servedLocalProbe   = 2 * time.Second
)

// probeServed checks each mapping of name, both halves. Each connection is
// opened and closed at once: what reaches the session's process is a
// connection with nothing on it, the way a health check is.
func probeServed(tr clusterDialer, name string, ports []servedPort) []portProbe {
	out := make([]portProbe, 0, len(ports))
	for _, sp := range ports {
		pr := portProbe{port: sp}
		if c, err := tr.DialClusterTimeout(net.JoinHostPort(name, sp.cluster), servedClusterProbe); err != nil {
			pr.clusterErr = err
		} else {
			c.Close()
		}
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", sp.local), servedLocalProbe); err != nil {
			pr.localErr = err
		} else {
			c.Close()
		}
		out = append(out, pr)
	}
	return out
}

// servedSummary is the part of a line that says which session it is: enough to
// recognise it as yours, and the PID to stop it.
func servedSummary(r *servedRecord) string {
	s := fmt.Sprintf("PID %d, started %s", r.pid, roughAge(r.age))
	if r.cmd != "" {
		s += ": " + r.cmd
	}
	return s
}

// servedVerdict turns what the probes found into the one line doctor prints
// for a served name. probes nil means nothing was checked, and why says so.
// Pure, so every way a name can fail to answer is testable without a cluster.
//
//   - the cluster does not answer: callers get nothing, whatever runs here.
//     The session's forward is gone (a sleep past the keepalive, an agent
//     restarted) or the name was taken from it: a fail, and the session to
//     restart;
//   - the cluster answers, nothing listens here: the name reaches this
//     machine and is refused there, which is the command not listening on the
//     port it was given (or not yet): a warning, since a server still starting
//     is the usual case;
//   - both answer: ok.
func servedVerdict(area, name string, r *servedRecord, probes []portProbe, why string) check {
	c := check{area: area, name: "served " + name}
	if probes == nil {
		c.status = stSkip
		c.detail = servedSummary(r) + " (not checked: " + why + ")"
		return c
	}
	var clusterDown, localDown []string
	var ok []string
	for _, p := range probes {
		switch {
		case p.clusterErr != nil:
			clusterDown = append(clusterDown, name+":"+p.port.cluster)
		case p.localErr != nil:
			localDown = append(localDown, p.port.local)
		default:
			ok = append(ok, name+":"+p.port.cluster+" -> localhost:"+p.port.local)
		}
	}
	switch {
	case len(clusterDown) > 0:
		c.status = stFail
		c.detail = strings.Join(clusterDown, ", ") + " does not answer in the cluster; " + servedSummary(r)
		c.remedy = fmt.Sprintf("restart that session (kill %d, then run it again): its name no longer leads to it", r.pid)
	case len(localDown) > 0:
		c.status = stWarn
		c.detail = "the name answers, but nothing listens on localhost:" + strings.Join(localDown, ", localhost:") +
			"; " + servedSummary(r)
		c.remedy = "the command of PID " + fmt.Sprint(r.pid) + " has not opened localhost:" + localDown[0] +
			" (yet): check the port it listens on is the last field of -s"
	default:
		c.status = stOK
		c.detail = strings.Join(ok, ", ") + " answers; " + servedSummary(r)
	}
	return c
}

// doctorServed adds one line per live session serving a name under profile,
// probed through tr. tr nil means doctor could not open the tunnel: the
// sessions are still listed, since what is holding a name is worth knowing most
// when the cluster looks unreachable.
func doctorServed(profile, hostport string, tr clusterDialer, add func(check)) {
	entries := listServed()
	if len(entries) == 0 {
		return
	}
	profiles := map[string]bool{}
	for _, p := range listProfiles() {
		profiles[p] = true
	}
	for _, e := range entries {
		if !servedBelongsTo(e.rec, profile, hostport, profiles) {
			continue
		}
		switch {
		case len(e.rec.ports) == 0:
			add(servedVerdict(profile, e.name, e.rec, nil, "the session did not record its ports"))
		case tr == nil:
			add(servedVerdict(profile, e.name, e.rec, nil, "no tunnel to the agent"))
		default:
			add(servedVerdict(profile, e.name, e.rec, probeServed(tr, e.name, e.rec.ports), ""))
		}
	}
}

// doctorServedElsewhere lists, with the machine's own checks, the live sessions
// that no profile accounts for: written by an older core, which recorded no
// cluster, or serving a cluster no profile points at. They hold a name all the
// same, and someone looking for what holds it should find them.
func doctorServedElsewhere(add func(check)) {
	entries := listServed()
	if len(entries) == 0 {
		return
	}
	addrs := profileAddrs()
	profiles := map[string]bool{}
	for p := range addrs {
		profiles[p] = true
	}
	for _, e := range entries {
		claimed := false
		for p, hp := range addrs {
			if servedBelongsTo(e.rec, p, hp, profiles) {
				claimed = true
				break
			}
		}
		if claimed {
			continue
		}
		why := "the session recorded no cluster"
		if e.rec.cluster != "" {
			why = "no profile points at " + e.rec.cluster
		}
		add(servedVerdict("local", e.name, e.rec, nil, why))
	}
}
