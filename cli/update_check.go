package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// One state file PER CLUSTER: the policy is per profile, so two clusters must
// not overwrite each other's answer. Keyed by host:port, which is all the core
// knows about the cluster it serves.
func updateStatePath(cfg config) string {
	return filepath.Join(plugDir(), "update-"+tun.ClusterHash(cfg.host+":"+cfg.port))
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

	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, tun.SharedKnownHosts(), nil)
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

// probeUpdate returns the release available beyond what the agent runs, the
// image it was judged against, and whether the question could be answered at
// all. It answers nothing for a moving tag (latest, a branch): whether one of
// those has moved is a digest question only the cluster can settle, and paying
// ~31s in the background to find out is not worth it.
func probeUpdate(cfg config) (found, img string, ok bool) {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, tun.SharedKnownHosts(), nil)
	if err != nil {
		return "", "", false
	}
	defer tr.Close()

	before, err := tr.Exec("version")
	if err != nil || before == "" || strings.HasPrefix(before, "error:") {
		return "", "", false
	}
	out, err := tr.Exec("info")
	if err != nil || !strings.HasPrefix(out, "version=") {
		return "", "", false // an agent from before `info` cannot name its image
	}
	for _, f := range strings.Fields(out) {
		if v, cut := strings.CutPrefix(f, "image="); cut {
			img = v
		}
	}
	if img == "" {
		return "", "", false
	}
	host, repo, _ := parseImageRef(img)
	tags, err := registryTags(host, repo)
	if err != nil {
		return "", "", false
	}
	apply, current, errMsg, delegate := decideClient(img, before, "", tags)
	if delegate || errMsg != "" {
		return "", "", false // a moving tag, or a dev agent: not decidable from here
	}
	if current != "" {
		return "", img, true // up to date, and that IS an answer worth recording
	}
	return apply, img, true
}

// announceUpdate is the launcher's half: say what a previous session found, on
// the way past. Only in notify — auto is applied by the core, which is the only
// side that outlives the exec.
func announceUpdate(cfg config) {
	if normalizeUpdateMode(cfg.updateMode) != updateNotify {
		return
	}
	if st := loadUpdateState(cfg); st.available != "" {
		info("agent update available: v%s — run `plug update` to take it "+
			"(plug config update=auto to apply it for you, =none to stop saying it)", st.available)
	}
}
