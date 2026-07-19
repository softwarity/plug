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
	var profile, host, port string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			profile = flagValue(args, &i)
		case "-H", "--host":
			host = flagValue(args, &i)
		case "--port":
			port = flagValue(args, &i)
		default:
			fatal("usage: plug update [-p profile]")
		}
	}
	cfg, label := updateTarget(profile, host, port)

	before, err := agentVersion(cfg)
	if err != nil {
		fatal("cannot reach the agent at %s:%s: %v", cfg.host, cfg.port, err)
	}
	info("agent v%s at %s", before, label)

	after := before
	verdict := askSelfUpdate(cfg)
	switch updateWord(verdict) {
	case "updating":
		info("agent: %s", verdict)
		after = waitNewVersion(cfg, before)
		if after == before {
			info("agent still v%s after the refresh — already the newest under its tag, or the deployment pins an exact version (point it at a moving tag like softwarity/plug:latest to follow releases)", before)
		} else {
			info("agent updated: v%s → v%s", before, after)
		}
	case "current", "pulled":
		info("agent: %s", verdict)
	case "static":
		info("the agent has no orchestrator access (no docker.sock, no k8s RBAC) — update it by redeploying the softwarity/plug image (compose: docker compose pull && docker compose up -d; k8s: kubectl rollout restart)")
	default:
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

// askSelfUpdate runs the agent's self-update verb over the tunnel user and
// returns its one-line verdict.
func askSelfUpdate(cfg config) string {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, tun.SharedKnownHosts(), nil)
	if err != nil {
		fatal("tunnel user: %v — the agent image may be too old; redeploy softwarity/plug", err)
	}
	defer tr.Close()
	info("asking the agent to refresh itself (a registry pull can take a minute)…")
	out, err := tr.Exec("self-update")
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
