package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// The check runs while a session is up and its result is read by the NEXT one.
// It cannot be otherwise: the launcher execs the core and is gone, so anything
// it started in the background dies with it, and the core is holding the user's
// command — it must not stop to ask a registry anything.
//
// The lookup runs from THIS machine rather than from the agent, which is not an
// accident: `plug update` learned the hard way that a registry round-trip from
// the cluster VM costs ~31s against ~1s here (see clientSideUpdate).
const updateCheckEvery = 24 * time.Hour

// startupSettle is how long the background check waits before its first lookup,
// so the TUN and the resolver it will use are up. Measured need is a second or
// two; ten is comfortable and costs nothing — the result is for the NEXT launch.
const startupSettle = 10 * time.Second

// One state file PER CLUSTER: the policy is per profile, so two clusters must
// not overwrite each other's answer. Keyed by host:port, which is all the core
// knows about the cluster it serves — hashed rather than used raw, since a host
// can carry anything a filename cannot.
//
// Hashed here rather than with tun.ClusterHash, which is a darwin||windows file:
// this path runs on all three.
func updateStatePath(cfg config) string {
	sum := sha256.Sum256([]byte(cfg.host + ":" + cfg.port))
	return filepath.Join(plugDir(), "update-"+hex.EncodeToString(sum[:8]))
}

// shouldCheck is the whole policy, kept apart from the network so it can be
// stated plainly: none never asks, and nobody asks more than once a day —
// someone who runs plug fifty times before lunch must not hit a registry fifty
// times. A state file that is missing or unreadable reads as "never checked",
// which is the right answer: check.
func shouldCheck(mode string, st updateState, now time.Time) bool {
	if mode == updateNone {
		return false
	}
	return now.Sub(st.checked) >= updateCheckEvery
}

type updateState struct {
	checked   time.Time
	available string // the release the registry has and the agent does not
	image     string // what that was decided against
}

func loadUpdateState(cfg config) updateState {
	var st updateState
	data, err := os.ReadFile(updateStatePath(cfg))
	if err != nil {
		return st
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "checked":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				st.checked = time.Unix(n, 0)
			}
		case "available":
			st.available = v
		case "image":
			st.image = v
		}
	}
	return st
}

func saveUpdateState(cfg config, st updateState) {
	path := updateStatePath(cfg)
	guardUserPath(path) // the core may hold root here — never write outside the caller's tree
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	body := fmt.Sprintf("checked=%d\navailable=%s\nimage=%s\n",
		st.checked.Unix(), st.available, st.image)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return
	}
	chownToUser(path) // written as euid 0 on the setuid path
}

// backgroundUpdateCheck asks, at most once a day, whether the registry carries a
// release this cluster's agent does not. Started by the core in its own
// goroutine and left to run: it never blocks the session, never prints, and a
// failure of any kind is simply not an answer — the state keeps whatever it
// held, and the next session asks again.
func backgroundUpdateCheck(cfg config) {
	defer func() { _ = recover() }() // a background nicety must never take the session down

	if !shouldCheck(normalizeUpdateMode(cfg.updateMode), loadUpdateState(cfg), time.Now()) {
		return
	}
	// Let the datapath settle first. This goroutine starts while the core is
	// still bringing up the TUN and repointing the resolver, and its own lookup
	// of the registry goes THROUGH that resolver — fired too early it gets
	// "no such host" for a name that resolves perfectly a moment later, and the
	// check silently records nothing. Nobody is waiting on this, so waiting is
	// free; being early is not.
	time.Sleep(startupSettle)
	found, img, ok := probeUpdate(cfg)
	if !ok {
		return
	}
	saveUpdateState(cfg, updateState{checked: time.Now(), available: found, image: img})

	// auto applies it from HERE rather than from the launcher, because the
	// launcher execs the core and is gone — nothing it starts in the background
	// outlives it. Applying from the core also gives the behaviour asked for:
	// the agent rolls, every session drops, reconnects on its own, and each one
	// says on the way back that it is now the older side.
	if found != "" && normalizeUpdateMode(cfg.updateMode) == updateAuto {
		applyUpdate(cfg, found)
	}
}

// applyUpdate hands the agent an already resolved tag. Clearing the state first
// is what stops a rollout that fails, or one that is merely slow, from being
// retriggered by every later session.
func applyUpdate(cfg config, tag string) {
	saveUpdateState(cfg, updateState{checked: time.Now()})

	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, cfg.authKeys(), tun.SharedKnownHosts(), nil)
	if err != nil {
		return
	}
	defer tr.Close()
	verdict, err := tr.Exec("self-update apply " + tag)
	if err != nil || verdict == "" {
		return
	}
	info("agent update to v%s: %s", tag, verdict)
}

// updateNotice words what a previous session found. Two different findings
// arrive here through the same field, and they must not be worded the same:
//
//   - a RELEASE (2.9.4): there is a newer version, and it has a number.
//   - a moving TAG (latest, main, a branch): there is no newer number — the
//     stream simply points at different bytes than what the cluster runs. Saying
//     "update available: vlatest" would be nonsense, which is the trap of
//     carrying both in one string.
func updateNotice(available string) string {
	const how = "run `plug update` to take it (plug config update=auto to apply it for you, =none to stop saying it)"
	if _, ok := parseExactRelease(available); ok {
		return fmt.Sprintf("agent update available: v%s — %s", available, how)
	}
	return fmt.Sprintf("the agent follows %q and that tag now points at a different image — %s", available, how)
}

// probeUpdate returns the release available beyond what the agent runs, the
// image it was judged against, and whether the question could be answered at
// all. It answers nothing for a moving tag (latest, a branch): whether one of
// those has moved is a digest question only the cluster can settle, and paying
// ~31s in the background to find out is not worth it.
func probeUpdate(cfg config) (found, img string, ok bool) {
	var before string
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, cfg.authKeys(), tun.SharedKnownHosts(), nil)
	if err != nil {
		return "", "", false
	}
	defer tr.Close()

	// ONE verb, on the tunnel channel: `info` carries both the running version
	// and the image. Asking `version` here used to come first and killed the
	// whole check — that verb lives on the DOWNLOAD channel (the `get` user's
	// ForceCommand), not on the tunnel user's, so the agent answered
	// `error: unknown command "version"`, probeUpdate gave up on the spot and
	// nothing was ever recorded. The check never fired for anyone.
	out, err := tr.Exec("info")
	if err != nil || !strings.HasPrefix(out, "version=") {
		return "", "", false // an agent from before `info` cannot name its image
	}
	for _, f := range strings.Fields(out) {
		if v, cut := strings.CutPrefix(f, "version="); cut {
			before = v
		}
		if v, cut := strings.CutPrefix(f, "image="); cut {
			img = v
		}
	}
	if before == "" || img == "" {
		return "", "", false
	}
	host, repo, _ := parseImageRef(img)
	tags, err := registryTagsWithin(host, repo, 20*time.Second)
	if err != nil {
		// This machine cannot reach the registry. Until now that ended the check
		// — no answer, no message, and silence is indistinguishable from "you
		// are up to date". It is also the machine plug is most useful on: a
		// corporate network, a proxy, a VPN that splits routes. Measured on
		// GitHub's macOS runners, where Docker Hub times out.
		//
		// The cluster reaches the registry (it pulls from it), so ask the agent.
		// `plug update` has always had this fallback; the check never did.
		if av := askAgentForUpdate(tr); av != "" {
			return av, img, true
		}
		return "", "", false
	}
	apply, current, errMsg, delegate := decideClient(img, before, "", tags)
	if delegate || errMsg != "" {
		// A moving tag (latest, main, a branch) has no version to compare — but
		// it does have BYTES. The agent reports its image with the digest it
		// resolved to, so ask the registry what that tag points at now. This used
		// to be given up on ("only the cluster can settle it"), which left every
		// deployment following a stream permanently unchecked.
		if tag := movingTagOf(img); tag != "" {
			if here := imageDigest(img); here != "" {
				if there, derr := registryDigestWithin(host, repo, tag, 10*time.Second); derr == nil && there != here {
					return tag, img, true // the stream moved under this deployment
				}
			}
		}
		return "", "", false // a dev agent, or a stream we could not resolve
	}
	if current != "" {
		return "", img, true // up to date, and that IS an answer worth recording
	}
	return apply, img, true
}

// askAgentForUpdate asks the cluster what this deployment would move to, for
// when this machine cannot ask the registry itself.
//
// Returns the available tag, or "" for anything else — including an agent too
// old to know the verb, which answers `unknown command`. That case keeps the
// previous behaviour (say nothing) rather than inventing one, so an old cluster
// is no worse off than before.
func askAgentForUpdate(tr *tunnel.Transport) string {
	out, err := tr.Exec("check-update")
	if err != nil {
		return ""
	}
	return parseAgentUpdateAnswer(out)
}

// parseAgentUpdateAnswer reads the agent's one line. Split from the call so the
// contract can be tested without a cluster — and the contract is: trust nothing
// but an explicit "available <tag>". "current", an error, or the `unknown
// command` of an agent too old all mean the same thing here, which is say
// nothing.
func parseAgentUpdateAnswer(out string) string {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 2 || f[0] != "available" {
		return ""
	}
	return f[1]
}

// announceUpdate is the launcher's half: say what a previous session found, on
// the way past. Only in notify — auto is applied by the core, which is the only
// side that outlives the exec.
func announceUpdate(cfg config) {
	if normalizeUpdateMode(cfg.updateMode) != updateNotify {
		return
	}
	if st := loadUpdateState(cfg); st.available != "" {
		// Offer it when someone is actually there to answer; fall back to saying
		// it otherwise (a script, a CI job, a pipe).
		if !offerUpdate(cfg, st.available) {
			info("%s", updateNotice(st.available))
		}
	}
}

// offerUpdateDeadline is deliberately short. askToStop can afford two minutes:
// it interrupts someone who is already deciding something. This one interrupts
// nobody — it appears before a command the user typed and wants to run, so an
// unanswered prompt must cost seconds, not minutes.
const offerUpdateDeadline = 12 * time.Second

// offerUpdate asks whether to apply the update now, and applies it if told to.
// Returns whether it ran one.
//
// Guarded three ways, because the cost of a wrong "yes" is not local: applying
// rolls the AGENT, which is shared — on a common cluster every other developer's
// session reconnects. So the prompt says that out loud, the default is NO, and
// it never appears without a real terminal (the askToStop trap: a context we
// mistook for interactive once wedged a Windows leg for 16 minutes).
//
// The update runs as a SUBPROCESS rather than in-process: cmdUpdate calls
// fatal() on failure, and a failed update must never cost the user the command
// they actually typed.
func offerUpdate(cfg config, available string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	tty, err := os.Open(ttyDevice)
	if err != nil {
		return false
	}
	defer tty.Close()
	fmt.Fprintf(os.Stderr, "[plug] %s\n", updateNotice(available))
	fmt.Fprint(os.Stderr, "[plug] apply it now? it rolls the cluster's agent, and any other session on it reconnects [y/N]: ")
	answer := make(chan string, 1)
	go func() {
		line, rerr := bufio.NewReader(tty).ReadString('\n')
		if rerr != nil {
			line = "n"
		}
		answer <- strings.ToLower(strings.TrimSpace(line))
	}()
	var a string
	select {
	case a = <-answer:
	case <-time.After(offerUpdateDeadline):
		fmt.Fprintln(os.Stderr, "\n[plug] no answer — leaving the agent as it is")
		return false
	}
	if a != "y" && a != "yes" && a != "o" && a != "oui" {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	cmd := exec.Command(self, "update", "-H", cfg.host, "--port", cfg.port)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		info("the update did not complete (%v) — carrying on with your command", err)
	}
	return true
}
