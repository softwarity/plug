//go:build darwin

package tun

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Available reports whether the TUN data path can run on this OS.
func Available() bool { return true }

// defaultTUNName: wireguard-go assigns the next free utunN when given "utun".
const defaultTUNName = "utun"

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
// (privResolv is empty; the child runs directly). Phase 2 refuses a 2nd instance
// on macOS precisely because this resolver override is machine-wide.
func configure(_ any, ifname, cidr, dnsIP string, log logfn) ([]string, string, func(), error) {
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

	// Also register a DOMAIN-scoped resolver for ".plug" via /etc/resolver. When the
	// primary-service override lands INTERFACE-scoped (a headless runner, some VPN
	// setups), macOS won't send a general getaddrinfo query to it — but it DOES use a
	// domain-scoped resolver for a matching name. Paired with the "plug" search domain
	// above, getaddrinfo tries "<name>.plug", which this routes to us; answerDNS strips
	// it. Mirrors the Windows NRPT rule.
	resolverFile := "/etc/resolver/" + searchSuffix
	_ = os.MkdirAll("/etc/resolver", 0o755)
	_ = os.WriteFile(resolverFile, []byte("nameserver "+dnsIP+"\n"), 0o644)

	// Make mDNSResponder pick up the new resolvers and drop any stale (negative) cache.
	flushDNS := func() {
		_ = run("dscacheutil", "-flushcache")
		_ = run("killall", "-HUP", "mDNSResponder")
	}
	flushDNS()

	cleanup := func() {
		_ = os.Remove(resolverFile)
		if restore != "" {
			_ = scutilSet(dnsKey, restore) // put the original DNS dict back
		} else {
			_ = scutilRemove(dnsKey) // there was none — drop ours
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
	svc, err := primaryService()
	if err != nil {
		return err
	}
	dnsKey := "State:/Network/Service/" + svc + "/DNS"
	restore, _, _ := readDNSDict(dnsKey)
	return persistDNSBackup(backupPath(key), dnsKey, restore)
}

// RestoreOrphanDNS restores a leftover DNS backup — a crashed daemon left the
// system resolver pointed at a dead address — and removes it. No-op if none. Call
// only while holding the leader lock, so any backup present can only be an orphan.
func RestoreOrphanDNS(key string) {
	if _, err := os.Stat(backupPath(key)); err == nil {
		_ = restoreDNSBackup(backupPath(key))
	}
}

// ClearDNSBackup drops the backup after a clean shutdown (cleanup already restored the DNS).
func ClearDNSBackup(key string) { _ = os.Remove(backupPath(key)) }
