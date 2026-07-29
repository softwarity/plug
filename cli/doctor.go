package main

// plug doctor — read-only health checks for everything plug touches: the
// binaries (launcher, cached cores, the privileged service/daemon and ITS
// version — the one thing the per-cluster version mechanism does not refresh),
// the system state (a resolver still pointed at plug with no session alive),
// and each profile's cluster (agent reachable, version, dynamic-`-s` backend,
// NXDOMAIN-era agent or not). Every finding carries its remedy. When problems
// are found on an interactive terminal, doctor offers to open a pre-filled
// GitHub issue — the browser is the auth AND the review step (the repo is
// public: hostnames/IPs are redacted from the report first).

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

type checkStatus int

const (
	stOK checkStatus = iota
	stWarn
	stFail
	stSkip
)

type check struct {
	area   string // "local" | profile name
	name   string
	status checkStatus
	detail string // shown after the name; may contain hostnames (redacted in the issue)
	remedy string // one line, only for warn/fail
}

func (s checkStatus) glyph() string {
	switch s {
	case stOK:
		return "✓"
	case stWarn:
		return "!"
	case stFail:
		return "✗"
	default:
		return "·"
	}
}

func cmdDoctor(args []string) {
	only := ""
	fix := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			if i+1 >= len(args) {
				fatal("missing value for %s", args[i])
			}
			i++
			only = args[i]
		case "--fix":
			fix = true
		default:
			fatal("usage: plug doctor [-p profile] [--fix]")
		}
	}
	doctorFix = fix

	var checks []check
	add := func(c check) { checks = append(checks, c) }

	doctorLocal(add)

	profiles := listProfiles()
	if only != "" {
		profiles = []string{only}
	}
	for _, p := range profiles {
		doctorProfile(p, add)
	}
	if len(profiles) == 0 {
		add(check{area: "local", name: "profiles", status: stWarn,
			detail: "none configured", remedy: "plug -p <name> to create one (or install from a cluster)"})
	}

	// ---- render ----
	fails, warns := 0, 0
	area := ""
	for _, c := range checks {
		if c.area != area {
			area = c.area
			title := area
			if area != "local" {
				title = "profile " + area
			}
			fmt.Printf("\n%s\n", title)
		}
		fmt.Printf("  %s %-28s %s\n", c.status.glyph(), c.name, c.detail)
		if c.remedy != "" && c.status != stOK && c.status != stSkip {
			fmt.Printf("      → %s\n", c.remedy)
		}
		switch c.status {
		case stFail:
			fails++
		case stWarn:
			warns++
		}
	}
	fmt.Println()
	switch {
	case fails > 0:
		fmt.Printf("%d problem(s), %d warning(s).\n", fails, warns)
	case warns > 0:
		fmt.Printf("no problems, %d warning(s).\n", warns)
	default:
		fmt.Println("all good.")
	}

	if fails+warns > 0 {
		offerIssue(checks)
	}
	if fails > 0 {
		os.Exit(1)
	}
}

// doctorFix — --fix applies the SAFE repairs while checking: purge a
// truncated cached core (it re-downloads on next use), restore a resolver
// left stale with no session (plug down). Anything touching privileges, the
// user's own sessions or the cluster stays a printed remedy on purpose.
var doctorFix bool

// doctorLocal collects the machine-side checks: binaries, cache, privilege,
// the service/daemon, the system resolver state, Docker Desktop.
func doctorLocal(add func(check)) {
	// Launcher.
	self, _ := os.Executable()
	add(check{area: "local", name: "launcher", status: stOK,
		detail: fmt.Sprintf("v%s (%s)", version, self)})

	// Cached cores: a truncated download must never be trusted (ensureVersion
	// refuses them, but say it here too).
	if entries, err := os.ReadDir(versionsDir()); err == nil {
		var vers, broken []string
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := "plug"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			if fi, err := os.Stat(filepath.Join(versionsDir(), e.Name(), name)); err == nil && fi.Size() > 1<<20 {
				vers = append(vers, e.Name())
			} else {
				broken = append(broken, e.Name())
			}
		}
		st, detail, remedy := stOK, strings.Join(vers, ", "), ""
		if detail == "" {
			detail = "none yet"
		}
		if len(broken) > 0 {
			if doctorFix {
				for _, v := range broken {
					_ = os.RemoveAll(filepath.Join(versionsDir(), v))
				}
				detail += " — purged truncated: " + strings.Join(broken, ", ") + " (re-downloads on next use)"
			} else {
				st = stWarn
				detail += " — broken: " + strings.Join(broken, ", ")
				remedy = "rm -r " + versionsDir() + "/<broken> (it will re-download; or: plug doctor --fix)"
			}
		}
		add(check{area: "local", name: "cached cores", status: st, detail: detail, remedy: remedy})
	}

	// Per-OS: privilege grant, service/daemon state + version, resolver state.
	doctorOS(add)

	// Sessions currently registered (the graft/registry view).
	doctorSessions(add)
}

// doctorProfile checks one cluster: reachability + agent version over the get
// user, then — through the real tunnel — the agent's own `info` (which dynamic
// -s backend this deployment has) and the `resolve` probe (a pre-2.2 agent has
// neither, which also means no honest-NXDOMAIN and no -c support).
func doctorProfile(name string, add func(check)) {
	host, port, err := readProfileSoft(name)
	if err != nil {
		add(check{area: name, name: "profile", status: stFail,
			detail: err.Error(), remedy: "plug rm " + name + " (or fix ~/.plug/" + name + ".conf)"})
		return
	}
	cfg := config{host: host, port: port}
	ver, err := agentVersion(cfg)
	if err != nil {
		add(check{area: name, name: "agent", status: stFail,
			detail: fmt.Sprintf("unreachable at %s:%s", host, port),
			remedy: "is the cluster up? plug test " + name})
		return
	}
	add(check{area: name, name: "agent", status: stOK,
		detail: fmt.Sprintf("v%s at %s:%s", ver, host, port)})

	tr, err := tunnel.Dial(host, port, sshUser, embeddedKey, tun.SharedKnownHosts(), nil)
	if err != nil {
		add(check{area: name, name: "tunnel user", status: stFail,
			detail: err.Error(), remedy: "the agent image may be too old — redeploy softwarity/plug"})
		return
	}
	defer tr.Close()

	if out, err := tr.Exec("info"); err == nil && strings.HasPrefix(out, "version=") {
		backend := "none"
		for _, f := range strings.Fields(out) {
			if v, ok := strings.CutPrefix(f, "backend="); ok {
				backend = v
			}
		}
		if backend == "none" {
			add(check{area: name, name: "-s names", status: stFail,
				detail: "the agent cannot create cluster names",
				remedy: "mount /var/run/docker.sock (on Swarm, on a manager node), or apply plug-k8s.yaml"})
		} else {
			add(check{area: name, name: "-s names", status: stOK, detail: backend})
		}
		add(check{area: name, name: "agent features", status: stOK,
			detail: "≥ 2.2 (honest NXDOMAIN, -c, takeover)"})
	} else {
		add(check{area: name, name: "agent features", status: stWarn,
			detail: "pre-2.2 agent (no NXDOMAIN check, no -c)",
			remedy: "redeploy the softwarity/plug image, sessions pick it up on restart"})
	}
}

// readProfileSoft reads a profile without loadProfile's fatal — doctor reports
// a broken profile as a finding, it must not die on it.
func readProfileSoft(name string) (host, port string, err error) {
	data, err := os.ReadFile(filepath.Join(plugDir(), name+".conf"))
	if err != nil {
		return "", "", fmt.Errorf("unreadable: %v", err)
	}
	port = defaultPort
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "host":
			host = strings.TrimSpace(val)
		case "port":
			port = strings.TrimSpace(val)
		}
	}
	if host == "" {
		return "", "", fmt.Errorf("no host in the profile")
	}
	return host, port, nil
}

// ---- the GitHub issue offer ----

// offerIssue proposes opening a pre-filled issue when doctor found something
// and stdin is a terminal. The BROWSER is both the auth and the review step:
// the user sees (and can edit) exactly what would be posted — important, since
// the repo is public and the report describes their infrastructure. Hostnames
// and IPs are redacted from the body beforehand.
func offerIssue(checks []check) {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return // not interactive — never block a script
	}
	fmt.Print("report this to github.com/softwarity/plug as an issue? [y/N] ")
	var ans string
	fmt.Scanln(&ans)
	if a := strings.ToLower(strings.TrimSpace(ans)); a != "y" && a != "yes" {
		return
	}
	u := issueURL(checks)
	fmt.Println("opening (review and edit before submitting):")
	fmt.Println("  " + u)
	openBrowser(u)
}

// issueURL builds the pre-filled new-issue URL: a summary, not a dump — only
// the warn/fail lines, hostnames/IPs redacted, plus the environment header.
func issueURL(checks []check) string {
	var b strings.Builder
	fmt.Fprintf(&b, "`plug doctor` report — plug v%s, %s/%s\n\n", version, runtime.GOOS, runtime.GOARCH)
	b.WriteString("```\n")
	seq := map[string]int{}
	for _, c := range checks {
		if c.status != stWarn && c.status != stFail {
			continue
		}
		area := c.area
		if area != "local" {
			if _, ok := seq[area]; !ok {
				seq[area] = len(seq) + 1
			}
			area = fmt.Sprintf("cluster-%d", seq[area])
		}
		fmt.Fprintf(&b, "%s [%s] %s: %s\n", c.status.glyph(), area, c.name, redact(c.detail))
		if c.remedy != "" {
			fmt.Fprintf(&b, "    → %s\n", redact(c.remedy))
		}
	}
	b.WriteString("```\n\n(what were you doing / anything to add?)\n")
	return "https://github.com/softwarity/plug/issues/new?title=" +
		url.QueryEscape("doctor: ") + "&body=" + url.QueryEscape(b.String())
}

// redact masks IPs and likely hostnames (dotted names that are not paths) so
// an internal topology never lands in a public issue by reflex. Ports survive:
// "10.2.3.4:2222" → "x.x.x.x:2222", "neo.corp:2222" → "redacted.host:2222".
func redact(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		host := f
		if h, p, ok := strings.Cut(f, ":"); ok && !strings.Contains(p, "/") {
			host = h
		}
		trimmed := strings.Trim(host, "(),")
		if trimmed == "" || strings.ContainsAny(trimmed, "/~") {
			continue // a path, never a hostname
		}
		switch {
		case net.ParseIP(trimmed) != nil:
			fields[i] = strings.Replace(f, trimmed, "x.x.x.x", 1)
		case strings.Contains(trimmed, ".") && !looksLikeVersion(trimmed):
			fields[i] = strings.Replace(f, trimmed, "redacted.host", 1)
		}
	}
	return strings.Join(fields, " ")
}

// fileExists is the tiny stat wrapper the per-OS checks share.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// looksLikeVersion tells "v2.1.0"/"2.1.0" apart from a hostname — versions are
// dots and digits only (an optional leading v), and must stay readable in the
// issue body.
func looksLikeVersion(s string) bool {
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

func openBrowser(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	if err := c.Start(); err != nil {
		fmt.Println("(could not open a browser — copy the URL above)")
	}
}
