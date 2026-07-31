//go:build darwin

package tun

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Available reports whether the TUN data path can run on this OS.
func Available() bool { return true }

// defaultTUNName: wireguard-go assigns the next free utunN when given "utun".
const defaultTUNName = "utun"

// One datapath per machine (the global daemon): a single instance slot. The OS
// already auto-assigns the utunN device name.
const maxInstances = 1

func tunNameFor(int) string { return defaultTUNName }

func checkPriv() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("plug --tun needs root (create utun + set routes): run with sudo, or install the plug helper")
	}
	return nil
}

// configure brings the utun up as a point-to-point link, routes the instance's
// /24 into it, and repoints the SYSTEM resolver at dnsIP through the
// SystemConfiguration dynamic store — NOT /etc/resolv.conf, which macOS ignores
// (getaddrinfo resolves via mDNSResponder/SystemConfiguration). We override the
// PRIMARY network service's State:.../DNS ServerAddresses; that resolver is
// non-scoped, so it answers bare single-label cluster names too. The dynamic
// store is volatile (a reboot or network change resets it) — the crash-safety we
// want. cleanup restores the captured dict (or removes ours if there was none).
//
// macOS has no mount namespace, so this repoint is global for the session
// (privResolv is empty; the child runs directly). Machine-wide DNS is what the
// PID-at-connect multicluster model uses anyway — one resolver hands out fake
// IPs and the owning cluster is resolved at connect() (see route_darwin's
// resolvConf note) — proven simultaneously in CI.
func configure(_ any, _ int, ifname, cidr, dnsIP string, log logfn) ([]string, string, func(), error) {
	for _, cmd := range [][]string{
		{"ifconfig", ifname, "inet", "10.99.99.1", "10.99.99.2", "up"},
		{"route", "-n", "add", "-net", cidr, "-interface", ifname},
	} {
		if err := run(cmd[0], cmd[1:]...); err != nil {
			return nil, "", func() {}, err
		}
	}

	delRoute := func() { _ = run("route", "-n", "delete", "-net", cidr, "-interface", ifname) }

	svc, err := primaryService()
	if err != nil {
		log.f("tun[mac]: no primary network service (%v) — cluster names may not resolve", err)
		return nil, "", delRoute, nil
	}

	dnsKey := "State:/Network/Service/" + svc + "/DNS"
	restore, upstreams, search := readDNSDict(dnsKey)

	// Become the primary resolver AND advertise a ".plug" search domain (keeping the
	// user's existing ones). Override ServerAddresses with dnsIP — dotted names still
	// work, answerDNS forwards them to the captured upstream. The SearchDomains matter
	// for BARE single-label names: macOS does not send an unqualified name to a
	// resolver that has no search domain (it treats it as mDNS/.local), so on a network
	// without one — e.g. a headless CI runner — "my-service" never reaches us. With
	// "plug" appended, getaddrinfo also tries "my-service.plug", which lands here and
	// answerDNS strips back to the bare name. Same mechanism as the Windows NRPT suffix.
	searchList := append(append([]string{}, search...), searchSuffix)
	set := "d.init\nd.add ServerAddresses * " + dnsIP + "\nd.add SearchDomains * " + strings.Join(searchList, " ") + "\n"
	if err := scutilSet(dnsKey, set); err != nil {
		log.f("tun[mac]: could not repoint system DNS (%v) — cluster names may not resolve", err)
	}

	// MANUALLY configured DNS servers live in Setup:/Network/Service/<svc>/DNS, and
	// the composite resolver prefers Setup: over State: — so with manual DNS (a very
	// common dev setup) our State: override loses its ServerAddresses and libresolv
	// clients keep querying the user's servers → NXDOMAIN on cluster names. Go's
	// darwin resolver IS libresolv (it ignores both /etc/resolver and resolv.conf),
	// which is why only go clients failed while getaddrinfo languages resolved via
	// /etc/resolver/plug. Repoint Setup: too when it defines servers (restored on
	// teardown; crash net in SaveDNSBackup/RestoreOrphanDNS).
	setupKey := "Setup:/Network/Service/" + svc + "/DNS"
	setupRestore, setupServers, setupSearch := readDNSDict(setupKey)
	setupOverridden := len(setupServers) > 0
	var setupSet string
	if setupOverridden {
		list := append(append([]string{}, setupSearch...), searchSuffix)
		setupSet = "d.init\nd.add ServerAddresses * " + dnsIP + "\nd.add SearchDomains * " + strings.Join(list, " ") + "\n"
		if err := scutilSet(setupKey, setupSet); err != nil {
			log.f("tun[mac]: could not repoint manual (Setup:) DNS (%v) — static-binary clients may not resolve", err)
		}
		if len(upstreams) == 0 {
			upstreams = setupServers // the manual servers are the real upstream for dotted names
		}
	}

	// Write the GLOBAL DNS key too. On some setups (headless runners at least) the
	// per-service override lands interface-SCOPED, leaving no usable DEFAULT
	// resolver: getaddrinfo then burns a fixed ~5s per single-label lookup (mDNS
	// detour) before falling through to the .plug scoped resolver — measured
	// 5.03s/5.02s on the runners, which is exactly what killed every client with a
	// 5s timeout. Global/DNS is what the composite default resolver is built from;
	// writing it makes plug's DNS answer FIRST (~ms). Volatile like the rest of
	// State:, watched by the watchdog, restored on teardown, crash-netted below.
	globalDNSKey := "State:/Network/Global/DNS"
	globalRestore, _, _ := readDNSDict(globalDNSKey)
	if err := scutilSet(globalDNSKey, set); err != nil {
		log.f("tun[mac]: could not set the global DNS (%v) — single-label lookups may be slow", err)
	}

	// Also register a DOMAIN-scoped resolver for ".plug" via /etc/resolver. When the
	// primary-service override lands INTERFACE-scoped (a headless runner, some VPN
	// setups), macOS won't send a general getaddrinfo query to it — but it DOES use a
	// domain-scoped resolver for a matching name. Paired with the "plug" search domain
	// above, getaddrinfo tries "<name>.plug", which this routes to us; answerDNS strips
	// it. Mirrors the Windows NRPT rule.
	resolverFile := "/etc/resolver/" + searchSuffix
	_ = os.MkdirAll("/etc/resolver", 0o755)
	_ = os.WriteFile(resolverFile, []byte("nameserver "+dnsIP+"\n"), 0o644)

	// Also point /etc/resolv.conf at us. getaddrinfo (node/python/java) already
	// resolves via the scoped resolver above, but a program with its OWN resolver —
	// Go's pure-Go resolver, used by CGO_ENABLED=0 static binaries — reads only this
	// file, and would otherwise NXDOMAIN on cluster names against the original
	// upstream. The crash net is in SaveDNSBackup / RestoreOrphanDNS.
	resolvSnap := snapshotResolv()
	writeResolv(dnsIP)

	// Known limit, accepted: without a usable DEFAULT resolver (headless runners
	// at least), mDNSResponder tries a bare "my-service" over mDNS (.local) FIRST
	// and only falls to the ".plug" search domain after a fixed ~5s (measured
	// 5.02s±0.01 while plug's own DNS answered in 25ms). Fighting that inside
	// mDNSResponder failed three ways (Global/DNS write, resolv.conf, and the
	// AlwaysAppendSearchDomains pref — measured inoperative), so Go children are
	// instead routed to the pure-Go resolver via GODEBUG (see goResolverEnv);
	// getaddrinfo clients absorb the one-time stall with their own retries.

	// Make mDNSResponder pick up the new resolvers and drop any stale (negative) cache.
	flushDNS := func() {
		_ = run("dscacheutil", "-flushcache")
		_ = run("killall", "-HUP", "mDNSResponder")
	}
	flushDNS()

	// Watchdog: the State: DNS dict is VOLATILE — configd re-derives it on network
	// events (a DHCP lease renewal, a reachability change), silently replacing our
	// override while the daemon lives. One overwrite would leave every subsequent
	// session without cluster DNS until `plug down`. So re-assert the override
	// whenever it goes missing — the DNS sibling of the transport's self-heal.
	//
	// Re-assert QUIETLY when possible. Flushing the cache + HUPping mDNSResponder
	// on every re-assert turned a chatty configd (a locationd Wi-Fi-scan loop
	// re-publishing the DHCP lease ~2/min, observed live) into a resolver that
	// restarts all day — which intermittently failed UNRELATED lookups
	// machine-wide. The Service key is only an INPUT to the composite config: as
	// long as Global (and Setup, when overridden) still point at us, what
	// resolution consumes never changed, so rewrite the input without touching the
	// resolver. A flush is due only when the EFFECTIVE config diverged — and even
	// then coalesced through flushGate.
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		gate := flushGate{window: 30 * time.Second}
		quiet := newLogLimiter(5 * time.Minute)
		for {
			select {
			case <-stopWatch:
				return
			case <-t.C:
				// WHICH service is primary is not settled once and for all. A
				// laptop that wakes, joins another network or drops a corporate
				// VPN gets a NEW primary service, and the old one's DNS dict —
				// which is where we faithfully keep writing — stops being what
				// resolution consumes. Everything then looks healthy from here:
				// the key we watch still holds our IP, so the checks below pass
				// and nothing is logged, while single-label names quietly go to
				// the DHCP resolver and come back NXDOMAIN, machine-wide, until
				// the daemon is restarted. Re-resolve it every tick, and move.
				if cur, cerr := primaryService(); cerr == nil && cur != svc {
					log.f("tun[mac]: the primary network service changed (%s → %s) — moving the DNS override; "+
						"cluster names resolved through the old one until now", svc, cur)
					if restore != "" {
						_ = scutilSet(dnsKey, restore) // hand the old service back
					}
					if setupOverridden && setupRestore != "" {
						_ = scutilSet(setupKey, setupRestore)
					}
					svc = cur
					dnsKey = "State:/Network/Service/" + svc + "/DNS"
					setupKey = "Setup:/Network/Service/" + svc + "/DNS"
					var newSearch, newSetupSearch, newSetupServers []string
					restore, _, newSearch = readDNSDict(dnsKey)
					setupRestore, newSetupServers, newSetupSearch = readDNSDict(setupKey)
					setupOverridden = len(newSetupServers) > 0
					set = "d.init\nd.add ServerAddresses * " + dnsIP + "\nd.add SearchDomains * " +
						strings.Join(append(append([]string{}, newSearch...), searchSuffix), " ") + "\n"
					if setupOverridden {
						setupSet = "d.init\nd.add ServerAddresses * " + dnsIP + "\nd.add SearchDomains * " +
							strings.Join(append(append([]string{}, newSetupSearch...), searchSuffix), " ") + "\n"
					}
					// The teardown below follows: it restores whatever dnsKey now
					// names. The on-disk crash net still snapshots the service
					// that was primary at startup, so a HARD crash after a move
					// leaves the new service pointed at us — `plug doctor` calls
					// that out (a plug resolver with no session) and `plug down`
					// clears it.
					gate.request()
				}
				input := false     // Service key — configd's composition input only
				effective := false // what resolution consumes: Global, Setup, the files
				if _, cur, _ := readDNSDict(dnsKey); len(cur) != 1 || cur[0] != dnsIP {
					_ = scutilSet(dnsKey, set)
					input = true
				}
				if _, cur, _ := readDNSDict(globalDNSKey); len(cur) != 1 || cur[0] != dnsIP {
					_ = scutilSet(globalDNSKey, set)
					effective = true
				}
				if setupOverridden {
					if _, cur, _ := readDNSDict(setupKey); len(cur) != 1 || cur[0] != dnsIP {
						_ = scutilSet(setupKey, setupSet)
						effective = true
					}
				}
				if b, err := os.ReadFile(resolverFile); err != nil || string(b) != "nameserver "+dnsIP+"\n" {
					_ = os.WriteFile(resolverFile, []byte("nameserver "+dnsIP+"\n"), 0o644)
					effective = true
				}
				if b, err := os.ReadFile(resolvConf); err != nil || !strings.Contains(string(b), dnsIP) {
					writeResolv(dnsIP)
					effective = true
				}
				if effective {
					gate.request()
				}
				if gate.due(time.Now()) {
					flushDNS()
					log.f("tun[mac]: effective DNS config was replaced — re-asserted (cache flushed)")
				} else if (input || effective) && quiet.allow("reassert") {
					log.f("tun[mac]: system DNS override was replaced (configd event?) — re-asserted quietly (repeats hidden 5m)")
				}
			}
		}
	}()

	cleanup := func() {
		close(stopWatch)
		<-watchDone // never re-assert after the restore below
		restoreResolv(resolvSnap)
		_ = os.Remove(resolverFile)
		if restore != "" {
			_ = scutilSet(dnsKey, restore) // put the original DNS dict back
		} else {
			_ = scutilRemove(dnsKey) // there was none — drop ours
		}
		if globalRestore != "" {
			_ = scutilSet(globalDNSKey, globalRestore)
		} else {
			_ = scutilRemove(globalDNSKey) // configd will recompose it from the services
		}
		if setupOverridden {
			_ = scutilSet(setupKey, setupRestore) // put the manual DNS back
		}
		flushDNS()
		delRoute()
	}
	return upstreams, "", cleanup, nil
}

// primaryService returns the id of the primary network service — the one whose
// DNS resolves bare names — from the dynamic store.
func primaryService() (string, error) {
	out, err := scutil("show State:/Network/Global/IPv4\nquit\n")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PrimaryService :"); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("no PrimaryService in State:/Network/Global/IPv4")
}

// readDNSDict reads the DNS dict at key and returns (a) a scutil script that
// rebuilds it verbatim (d.init + d.add lines) for restore, and (b) its
// ServerAddresses — plug's upstream for dotted names. Both are empty if the key
// is absent. It parses scutil's show output: scalars ("Key : value") and arrays
// ("Key : <array> { N : value ... }").
func readDNSDict(key string) (restore string, servers, search []string) {
	out, err := scutil("show " + key + "\nquit\n")
	if err != nil || strings.Contains(out, "No such key") {
		return "", nil, nil
	}
	var b strings.Builder
	b.WriteString("d.init\n")
	var curKey string
	var arr []string
	inArray := false
	flushArray := func() {
		if curKey == "" {
			return
		}
		b.WriteString("d.add " + curKey + " * " + strings.Join(arr, " ") + "\n")
		if curKey == "ServerAddresses" {
			servers = append(servers, arr...)
		}
		if curKey == "SearchDomains" {
			search = append(search, arr...)
		}
		curKey, arr, inArray = "", nil, false
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || strings.HasPrefix(line, "<dictionary>"):
			continue
		case strings.Contains(line, ": <array> {"):
			curKey = strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
			arr, inArray = nil, true
		case line == "}":
			if inArray {
				flushArray()
			}
		case inArray:
			if p := strings.SplitN(line, ":", 2); len(p) == 2 {
				arr = append(arr, strings.TrimSpace(p[1]))
			}
		default: // scalar "Key : value"
			if p := strings.SplitN(line, ":", 2); len(p) == 2 {
				b.WriteString("d.add " + strings.TrimSpace(p[0]) + " " + strings.TrimSpace(p[1]) + "\n")
			}
		}
	}
	return b.String(), servers, search
}

// scutil pipes a batch script into scutil (root; the plug core runs under sudo),
// used for dynamic-store reads and edits.
func scutil(script string) (string, error) {
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// scutilSet writes the dictionary accumulated by build (d.init/d.add lines) into
// key of the dynamic store.
func scutilSet(key, build string) error {
	_, err := scutil(build + "set " + key + "\nquit\n")
	return err
}

// scutilRemove deletes key from the dynamic store.
func scutilRemove(key string) error {
	_, err := scutil("remove " + key + "\nquit\n")
	return err
}

// resolvConf is macOS's /etc/resolv.conf. getaddrinfo ignores it (it resolves via
// SystemConfiguration), but a program with its OWN resolver — notably Go's pure-Go
// resolver, used by CGO_ENABLED=0 static binaries, common in clusters — reads only
// this file. Pointing it at plug's DNS is machine-wide, like the scutil override —
// which is what the PID-at-connect multicluster model wants anyway: one resolver
// hands out fake IPs, and the owning cluster is resolved at connect() by process
// ancestry (as on Windows) — proven simultaneously in CI.
var resolvConf = "/etc/resolv.conf" // overridable in tests

// snapshotResolv captures /etc/resolv.conf as a restorable token: "L\n<target>" for
// a symlink (the usual case — it points at /var/run/resolv.conf), "F\n<content>" for
// a regular file, or "N" if absent.
// flushGate coalesces DNS cache flushes: request() marks one pending, due()
// releases at most one per window. The first request after a quiet period fires
// immediately; a configd storm collapses into one flush per window instead of a
// resolver restart per event.
type flushGate struct {
	window  time.Duration
	last    time.Time
	pending bool
}

func (g *flushGate) request() { g.pending = true }

func (g *flushGate) due(now time.Time) bool {
	if !g.pending || now.Sub(g.last) < g.window {
		return false
	}
	g.pending = false
	g.last = now
	return true
}

func snapshotResolv() string {
	if target, err := os.Readlink(resolvConf); err == nil {
		return "L\n" + target
	}
	if data, err := os.ReadFile(resolvConf); err == nil {
		return "F\n" + string(data)
	}
	return "N"
}

// writeResolv points /etc/resolv.conf at plug's in-stack DNS with the ".plug" search
// suffix, as a FRESH regular file (replacing any symlink) so configd's own /var/run
// regeneration can't clobber it.
func writeResolv(dnsIP string) {
	_ = os.Remove(resolvConf)
	_ = os.WriteFile(resolvConf,
		[]byte("# plug — cluster DNS\nnameserver "+dnsIP+"\nsearch "+searchSuffix+"\n"),
		0o644)
}

// restoreResolv puts /etc/resolv.conf back from a snapshotResolv() token.
func restoreResolv(snap string) {
	_ = os.Remove(resolvConf)
	switch kind, rest, _ := strings.Cut(snap, "\n"); kind {
	case "L":
		_ = os.Symlink(rest, resolvConf)
	case "F":
		_ = os.WriteFile(resolvConf, []byte(rest), 0o644)
	}
}

// persistDNSBackup writes what's needed to restore the original DNS even after a
// kill -9 of the daemon: the primary-service DNS key on the first line, then the
// scutil rebuild script (`restore`). An empty script means the service had no DNS
// dict → restoring means REMOVING our override.
func persistDNSBackup(path, dnsKey, restore string) error {
	return os.WriteFile(path, []byte(dnsKey+"\n"+restore), 0o644)
}

// loadDNSBackup parses a backup file into its key and restore script.
func loadDNSBackup(path string) (dnsKey, restore string, err error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", "", e
	}
	dnsKey, restore, _ = strings.Cut(string(b), "\n")
	return dnsKey, restore, nil
}

// restoreDNSBackup replays the backup at path — re-applying the saved DNS dict, or
// removing our override if the service had none — then deletes the backup file.
func restoreDNSBackup(path string) error {
	dnsKey, restore, err := loadDNSBackup(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(restore) != "" {
		_ = scutilSet(dnsKey, restore)
	} else {
		_ = scutilRemove(dnsKey)
	}
	return os.Remove(path)
}

// SaveDNSBackup snapshots the current primary-service DNS into the cluster's
// backup file so a kill -9 of the daemon can be repaired. Call BEFORE the DNS is
// overridden (StartDatapath).
func SaveDNSBackup(key string) error {
	// Snapshot /etc/resolv.conf too — configure() restores it on a clean exit, this
	// is the net for a crashed daemon (restored by RestoreOrphanDNS).
	_ = os.WriteFile(resolvBackupPath(key), []byte(snapshotResolv()), 0o644)
	svc, err := primaryService()
	if err != nil {
		return err
	}
	// The MANUAL DNS dict (Setup:) is persistent — snapshot it whenever it defines
	// servers, since configure() will override it in that case.
	setupKey := "Setup:/Network/Service/" + svc + "/DNS"
	if setupRestore, setupServers, _ := readDNSDict(setupKey); len(setupServers) > 0 {
		_ = persistDNSBackup(setupBackupPath(key), setupKey, setupRestore)
	}
	dnsKey := "State:/Network/Service/" + svc + "/DNS"
	restore, _, _ := readDNSDict(dnsKey)
	return persistDNSBackup(backupPath(key), dnsKey, restore)
}

// RestoreOrphanDNS restores a leftover DNS backup — a crashed daemon left the
// system resolver pointed at a dead address — and removes it. No-op if none. Call
// only while holding the leader lock, so any backup present can only be an orphan.
func RestoreOrphanDNS(key string) {
	if b, err := os.ReadFile(resolvBackupPath(key)); err == nil {
		restoreResolv(string(b))
		_ = os.Remove(resolvBackupPath(key))
	}
	if _, err := os.Stat(setupBackupPath(key)); err == nil {
		_ = restoreDNSBackup(setupBackupPath(key)) // manual (Setup:) DNS back first
	}
	if _, err := os.Stat(backupPath(key)); err == nil {
		_ = restoreDNSBackup(backupPath(key))
		// Drop our Global/DNS override too — with the service dicts restored,
		// configd recomposes the correct global from them.
		_ = scutilRemove("State:/Network/Global/DNS")
	}
}

// ClearDNSBackup drops the backup after a clean shutdown (cleanup already restored the DNS).
func ClearDNSBackup(key string) {
	_ = os.Remove(backupPath(key))
	_ = os.Remove(resolvBackupPath(key))
	_ = os.Remove(setupBackupPath(key))
}
