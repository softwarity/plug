package main

// plug update — walk plug's distribution chain upstream. The agent's image IS
// the distribution point (the CLI installs FROM the agent), so an update is two
// hops: ask the agent to refresh ITSELF from its registry (each backend knows
// how — k8s rolls its Deployment, Swarm re-resolves its service tag, a plain
// container pulls and hands back the recreate command), then refresh THIS
// launcher from the now-current agent and re-apply the privileged grant
// (setuid/caps). On Windows nothing extra is needed: the datapath service
// starts on demand from the same plug.exe, so the next session simply runs the
// new binary — the service-vs-launcher version gap, closed.
//
// Live sessions ride it out by design: when the agent rolls, every transport
// reconnects and re-arms its -s forwards (the self-heal path) on the new agent.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

func cmdUpdate(args []string) {
	var profile, host, port, want string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			profile = flagValue(args, &i)
		case "-H", "--host":
			host = flagValue(args, &i)
		case "--port":
			port = flagValue(args, &i)
		default:
			// The one positional: which tag the deployment should carry from now
			// on. `tag` means the newest release; anything else is a tag name —
			// latest, main, feat-09 — and the agent checks it exists before
			// repointing anything.
			if strings.HasPrefix(args[i], "-") || want != "" {
				fatal("usage: plug update [-p profile] [<tag>|tag|latest]")
			}
			want = args[i]
		}
	}
	cfg, label := updateTarget(profile, host, port)

	before, err := agentVersion(cfg)
	if err != nil {
		fatal("cannot reach the agent at %s:%s: %v", cfg.host, cfg.port, err)
	}
	info("agent v%s at %s", shortVersion(before), label)

	// An agent from before this existed runs `self-update <tag>` as a plain
	// self-update: it ignores the word and refreshes along the tag it already
	// carries. That is the worst possible outcome — the channel silently does
	// not change while the command reports success — so refuse on the version
	// rather than let it happen.
	if want != "" {
		if versionBefore(before, 2, 5) {
			how := "plug update"
			if profile != "" {
				how = "plug -p " + profile + " update"
			}
			fatal("switching the channel needs an agent ≥ 2.5 — this cluster runs v%s, which would ignore\n"+
				"      %q and just refresh itself. Bring it up first: %s", before, want, how)
		}
		info("switching this cluster to %s", updateTargetWord(want))
	}
	after := before
	verdict := clientSideUpdate(cfg, before, want)
	if verdict == "" {
		verdict = askSelfUpdate(cfg, want)
	}
	switch updateWord(verdict) {
	case "updating":
		info("agent: %s", verdict)
		after = waitNewVersion(cfg, before)
		switch {
		case after != before:
			info("agent updated: v%s → v%s", shortVersion(before), shortVersion(after))
		case want != "":
			// Switching channels does not have to change the version — `latest`
			// and the newest release routinely name the same build. The image
			// moved either way, which is what was asked for; only say where to
			// look if the rollout is what has not landed.
			info("agent is on %s, still v%s — expected if that tag names the same build; "+
				"otherwise the rollout has not landed yet (docker service ps <service> / "+
				"kubectl rollout status deployment/<name>)", updateTargetWord(want), after)
		default:
			// The agent named the image it moved to before rolling, so a version
			// that has not changed is no longer "maybe a pin": the rollout itself
			// is stuck or slow. Point at where that is visible.
			info("agent still v%s after 90s — the rollout has not landed yet. Check it cluster-side "+
				"(docker service ps <service> / kubectl rollout status deployment/<name>): a pull that "+
				"cannot reach the registry, or a task that keeps restarting, both look like this.", before)
		}
	case "current", "pulled":
		info("agent: %s", verdict)
	default:
		// Two updates racing is already serialized cluster-side — Swarm's
		// service update carries the spec's version index (compare-and-swap),
		// so the loser gets "out of sequence". Say what that means instead of
		// relaying the rpc noise: someone else's update won, and won cleanly.
		if strings.Contains(verdict, "out of sequence") {
			fatal("another update reached the cluster first — let it finish, then check: plug version -p <profile>")
		}
		if strings.Contains(verdict, "unknown command") || strings.Contains(verdict, "not found") {
			// A pre-2.3 agent can't refresh itself — but it CAN still serve its
			// binaries, so the launcher hop below stays worth doing (align on
			// what this cluster runs today).
			info("the agent (v%s) predates self-update (needs plug ≥ 2.3) — redeploy the softwarity/plug image once; plug update takes over from there", before)
		} else {
			fatal("agent: %s", strings.TrimPrefix(verdict, "error: "))
		}
	}

	updateLauncher(cfg, after)
}

// updateWord is the agent's one-word verdict (the protocol's first token).
func updateWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// updateTarget resolves WHICH cluster to update: -H directly, -p by name, or
// the sole profile. Several profiles → name one (an update is a mutation; never
// guess the target). No wizard here — update meets existing clusters only.
func updateTarget(profile, host, port string) (config, string) {
	if host != "" {
		p := port
		if p == "" {
			p = defaultPort
		}
		return config{host: host, port: p}, host + ":" + p
	}
	name := profile
	if name == "" {
		names := listProfiles()
		switch len(names) {
		case 0:
			fatal("no profile configured — install from a cluster first, or: plug update -H <host>")
		case 1:
			name = names[0]
			info("using profile %q", name)
		default:
			fatal("several profiles (%s) — pick the cluster to update: plug update -p <name>", strings.Join(names, ", "))
		}
	}
	h, p, err := readProfileSoft(name)
	if err != nil {
		fatal("profile %q: %v", name, err)
	}
	if port != "" {
		p = port
	}
	return config{host: h, port: p}, fmt.Sprintf("%s (%s:%s)", name, h, p)
}

// clientSideUpdate is `plug update`'s fast path: ask the agent WHICH image it
// carries (info), resolve the target against that image's own registry from
// THIS machine, and hand the agent an already checked tag to APPLY. The lookup
// is the same either way — what changes is where it runs from: the agent's
// leaves the cluster through the Docker Desktop VM, whose DNS follows the
// plugged workstation's (measured: ~31s per registry round-trip); this
// process's rides the stub straight to the saved upstream (~1s).
//
// Returns the agent's verdict line, or "" to fall back to the agent-side
// lookup: an agent from before `info` named its image, a registry only the
// cluster can reach, a moving tag whose currentness is a digest question only
// the cluster can answer, or an agent from before `apply` existed.
func clientSideUpdate(cfg config, before, want string) string {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, tun.SharedKnownHosts(), nil)
	if err != nil {
		fatal("tunnel user: %v — the agent image may be too old; redeploy softwarity/plug", err)
	}
	defer tr.Close()
	out, err := tr.Exec("info")
	if err != nil || !strings.HasPrefix(out, "version=") {
		return ""
	}
	img := ""
	for _, f := range strings.Fields(out) {
		if v, ok := strings.CutPrefix(f, "image="); ok {
			img = v
		}
	}
	if img == "" {
		return ""
	}
	host, repo, _ := parseImageRef(img)
	tags, err := registryTags(host, repo)
	if err != nil {
		info("cannot list %s from this machine (%v) — asking the agent to do the lookup", repo, err)
		return ""
	}
	apply, current, errMsg, delegate := decideClient(img, before, want, tags)
	switch {
	case delegate:
		return ""
	case errMsg != "":
		fatal("%s", errMsg)
	case current != "":
		return "current " + current
	}
	verdict, err := tr.Exec("self-update apply " + apply)
	if err != nil {
		fatal("asking the agent: %v", err)
	}
	if strings.Contains(verdict, "usage: self-update") {
		return "" // an agent from before `apply` — let it do its own lookup
	}
	return verdict
}

// askSelfUpdate runs the agent's self-update verb over the tunnel user and
// returns its one-line verdict.
func askSelfUpdate(cfg config, want string) string {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, tun.SharedKnownHosts(), nil)
	if err != nil {
		fatal("tunnel user: %v — the agent image may be too old; redeploy softwarity/plug", err)
	}
	defer tr.Close()
	info("asking the agent to refresh itself (a registry pull can take a minute)…")
	verb := "self-update"
	if want != "" {
		verb += " " + want
	}
	out, err := tr.Exec(verb)
	if err != nil {
		fatal("asking the agent: %v", err)
	}
	if out == "" {
		fatal("no answer from the agent — it may predate self-update (needs plug ≥ 2.3): redeploy the softwarity/plug image once")
	}
	return out
}

// waitNewVersion polls the agent version while its redeploy rolls, and returns
// the first version that differs from before (early exit), or before after the
// timeout — the caller words that case honestly (already newest, or a pinned
// tag: the poll cannot tell those apart, only the registry knows).
func waitNewVersion(cfg config, before string) string {
	info("waiting for the new agent to come up…")
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		v, err := agentVersion(cfg)
		if err != nil {
			continue // mid-roll — keep waiting
		}
		if v != before {
			return v
		}
	}
	return before
}

// updateLauncher refreshes THIS binary from the agent when the agent is a newer
// release — never on a dev build (built from source, not distributed) and never
// DOWNward (a launcher from a newer cluster keeps serving an older one through
// the per-version core cache; replacing it would regress every other profile).
// Same x.y.z but a different +rev (a tag rebuilt in place) DOES replace: the
// launcher follows the agent's exact build, like the per-cluster cores do.
func updateLauncher(cfg config, remote string) {
	switch {
	case remote == version:
		info("launcher already matches the agent (v%s)", version)
		return
	case !semverOK(version):
		info("launcher is a dev build (%s) — not self-replacing; the agent is v%s", version, remote)
		return
	case !semverOK(remote):
		info("the agent runs a dev build (%s) — leaving the released launcher v%s in place", remote, version)
		return
	case semverLess(remote, version):
		info("launcher v%s is newer than this cluster's agent (v%s) — nothing to update locally (sessions to this cluster already run its exact core)", version, remote)
		return
	}
	self, err := os.Executable()
	if err != nil {
		fatal("%v", err)
	}
	data, err := getDownload(cfg, runtime.GOOS+"-"+runtime.GOARCH, "v"+remote)
	if err != nil {
		fatal("downloading v%s: %v", remote, err)
	}
	if len(data) < 1<<20 || !looksLikeBinary(data) {
		fatal("downloaded launcher looks invalid (%d bytes)", len(data))
	}
	if err := replaceBinary(self, data); err != nil {
		fatal("replacing %s: %v", self, err)
	}
	if runtime.GOOS == "windows" {
		// wintun.dll lives beside the exe and comes from the agent too.
		if dll, err := getDownload(cfg, "wintun", "wintun.dll"); err == nil && len(dll) > 100_000 {
			_ = os.WriteFile(filepath.Join(filepath.Dir(self), "wintun.dll"), dll, 0o644)
		}
	}
	regrantPrivilege(self)
	if out, err := exec.Command(self, "version").Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != remote {
			info("warning: %s answers v%s, expected v%s", self, v, remote)
			return
		}
	}
	info("launcher updated: v%s → v%s (%s)", version, remote, self)
}

// replaceBinary swaps target for data without ever leaving the path empty.
// Unix: rename over it — atomic, and allowed even when the current file is the
// root-owned setuid helper (renaming needs the DIRECTORY, which is ours).
// Windows: the running exe can't be overwritten but CAN be renamed aside, so
// park it as .old, slot the new one in, roll back if that fails.
func replaceBinary(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".plug-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	tmp.Chmod(0o755)
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Rename(tmp.Name(), target)
	}
	old := target + ".old"
	_ = os.Remove(old) // a previous update's leftover, if it unlocked since
	if err := os.Rename(target, old); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.Rename(old, target) // roll back — never leave no binary at all
		return err
	}
	_ = os.Remove(old) // best-effort: still locked while this process runs
	return nil
}

// regrantPrivilege re-applies what the install granted — a fresh file has none
// of it. One sudo on unix (prompted only when a terminal is there to answer);
// on Windows there is nothing to re-grant: the service's binPath still points
// at this exe, and the service starts on demand — the next session runs the
// new binary.
func regrantPrivilege(target string) {
	var grant string
	switch runtime.GOOS {
	case "windows":
		info("the datapath service starts on demand — new sessions run the new binary (current ones keep the old until they end)")
		return
	case "darwin":
		grant = fmt.Sprintf("chown root:wheel '%s' && chmod u+s '%s'", target, target)
	default:
		grant = fmt.Sprintf("setcap cap_net_admin,cap_sys_admin,cap_net_bind_service+ep '%s'", target)
	}
	if isTTY(os.Stdin) || isTTY(os.Stderr) {
		info("re-granting the privilege (one sudo)…")
		c := exec.Command("sudo", "sh", "-c", grant)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if c.Run() == nil {
			return
		}
	}
	info("re-grant the privilege to keep no-sudo runs:  sudo sh -c \"%s\"", grant)
}

// semverOK reports whether s is a released x.y.z version (dev builds are not).
func semverOK(s string) bool {
	_, ok := semverParse(s)
	return ok
}

// semverLess reports a < b, numerically per part — false when either side is
// not a release (never act on a comparison that means nothing).
func semverLess(a, b string) bool {
	va, oka := semverParse(a)
	vb, okb := semverParse(b)
	if !oka || !okb {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] < vb[i]
		}
	}
	return false
}

func semverParse(s string) ([3]int, bool) {
	var v [3]int
	s, _, _ = strings.Cut(s, "+") // released builds carry +<rev> metadata ("2.2.0+3503368")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// updateTargetWord renders the requested target for the one line printed before
// the agent is asked — "the newest release" reads as an intent, where the bare
// word `tag` would read as a tag literally named that.
func updateTargetWord(want string) string {
	if want == "tag" {
		return "the newest release"
	}
	return "the " + want + " tag"
}
