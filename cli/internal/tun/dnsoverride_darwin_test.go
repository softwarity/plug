//go:build darwin

package tun

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The DNS override's whole life, against a fake dynamic store: take the primary
// service over, follow the primary to another service, put everything back. The
// root selftest in CI proves the real store accepts what is written; this proves
// WHAT is written, and that nothing of plug's is left in the store afterwards,
// which the selftest cannot see from the inside.

// dynStore is a dynamic store a test can describe: the "show" output of each
// key, updated by the writes that go through scutilSet and scutilRemove so a
// read after a write sees the write, as configd would answer.
type dynStore struct {
	shown map[string]string // key -> scutil show output
	seq   []string          // every write, in order
	calls int               // scutil runs, reads included
}

func newDynStore() *dynStore { return &dynStore{shown: map[string]string{}} }

// dict renders a DNS dict as scutil prints it, from "Key = v1 v2" pairs.
func dict(pairs ...string) string {
	var b strings.Builder
	b.WriteString("<dictionary> {\n")
	for _, p := range pairs {
		k, v, _ := strings.Cut(p, " = ")
		b.WriteString("  " + k + " : <array> {\n")
		for i, x := range strings.Fields(v) {
			b.WriteString("    " + string(rune('0'+i)) + " : " + x + "\n")
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// fromScript turns a rebuild script (d.init / d.add lines, what readDNSDict
// returns and scutilSet is given) back into show output.
func fromScript(build string) string {
	var pairs []string
	for _, line := range strings.Split(build, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == "d.add" && f[2] == "*" {
			pairs = append(pairs, f[1]+" = "+strings.Join(f[3:], " "))
		}
	}
	return dict(pairs...)
}

// install stands the store behind the package's scutil primitives until the
// test ends.
func (s *dynStore) install(t *testing.T) {
	t.Helper()
	savedScutil, savedSet, savedRemove := scutil, scutilSet, scutilRemove
	t.Cleanup(func() { scutil, scutilSet, scutilRemove = savedScutil, savedSet, savedRemove })
	// Read scripts, one command or a batch: a show prints the key, d.show the
	// dictionary being built, which a read script only ever builds to separate
	// two outputs (scutilBatch), and a list the services' DNS keys, the one
	// pattern plug lists.
	scutil = func(script string) (string, error) {
		s.calls++
		var out strings.Builder
		for _, line := range strings.Split(script, "\n") {
			cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
			switch cmd {
			case "list":
				for i, k := range s.keys() {
					if strings.HasPrefix(k, "State:/Network/Service/") && strings.HasSuffix(k, "/DNS") {
						out.WriteString("  subKey [" + strconv.Itoa(i) + "] = " + k + "\n")
					}
				}
			case "show":
				if shown, ok := s.shown[arg]; ok {
					out.WriteString(shown)
				} else {
					out.WriteString("  No such key\n")
				}
			case "d.show":
				out.WriteString("<dictionary> {\n  " + batchSeparator + "\n}\n")
			}
		}
		return out.String(), nil
	}
	scutilSet = func(key, build string) error {
		s.seq = append(s.seq, "set "+key)
		s.shown[key] = fromScript(build)
		return nil
	}
	scutilRemove = func(key string) error {
		s.seq = append(s.seq, "remove "+key)
		delete(s.shown, key)
		return nil
	}
}

// servers reads a key's ServerAddresses back through the real parser.
func (s *dynStore) servers(key string) []string {
	_, servers, _ := readDNSDict(key)
	return servers
}

func (s *dynStore) keys() []string {
	var out []string
	for k := range s.shown {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestTheOverrideIsAppliedMovedAndTakenBackWhole(t *testing.T) {
	dir := t.TempDir()
	savedResolv, savedResolver := resolvConf, resolverPath
	t.Cleanup(func() { resolvConf, resolverPath = savedResolv, savedResolver })
	resolvConf = filepath.Join(dir, "resolv.conf")
	resolverPath = filepath.Join(dir, "resolver", searchSuffix)
	write(t, resolvConf, "nameserver 10.0.0.1\n")

	const dnsIP = "198.18.0.53"
	st := newDynStore()
	// Service X is primary, in DHCP (no manual entry); the global key is what
	// configd composed from it.
	st.shown["State:/Network/Service/X/DNS"] = dict("ServerAddresses = 10.0.0.1", "SearchDomains = corp.example")
	st.shown["State:/Network/Global/DNS"] = dict("ServerAddresses = 10.0.0.1")
	// Service Y is a VPN that will become primary later, with its own resolver.
	st.shown["State:/Network/Service/Y/DNS"] = dict("ServerAddresses = 10.8.0.1")
	st.install(t)

	up := newUpstream(nil)
	o := newMacDNSOverride("X", dnsIP, up, logfn(func(string, ...any) {}))

	// apply: the service, its manual entry and the global key all hold plug's
	// resolver, the files too, and the machine's own servers are the upstream.
	ups := o.apply()
	if strings.Join(ups, ",") != "10.0.0.1" {
		t.Fatalf("apply captured %v as the upstream, want the service's own 10.0.0.1", ups)
	}
	for _, k := range []string{"State:/Network/Service/X/DNS", "Setup:/Network/Service/X/DNS", "State:/Network/Global/DNS"} {
		if got := st.servers(k); len(got) != 1 || got[0] != dnsIP {
			t.Errorf("after apply, %s holds %v, want plug's %s alone", k, got, dnsIP)
		}
	}
	if b, _ := os.ReadFile(resolverPath); string(b) != "nameserver "+dnsIP+"\n" {
		t.Errorf("the scoped resolver file holds %q", b)
	}
	if b, _ := os.ReadFile(resolvConf); !strings.Contains(string(b), dnsIP) {
		t.Errorf("resolv.conf was not repointed: %q", b)
	}
	// Nothing was replaced on a key that lost it, so reassert is a no-op here.
	if input, effective := o.reassert(); input || effective {
		t.Errorf("reassert right after apply reported input=%v effective=%v, want neither: nothing moved", input, effective)
	}

	// moveTo: X gets its own dict back, its manual entry (which it never had)
	// goes away, Y's resolver becomes the upstream, and the next reassert
	// takes Y over. Setup: is the one that matters: it is persistent, and an
	// entry left on X would pin that service to a dead resolver across reboots.
	o.moveTo("Y")
	if got := st.servers("State:/Network/Service/X/DNS"); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("after the move, X holds %v, want its own 10.0.0.1 back", got)
	}
	if _, ok := st.shown["Setup:/Network/Service/X/DNS"]; ok {
		t.Error("after the move, X still has a manual (Setup:) entry it never had before plug")
	}
	if got := up.primary(); got != "10.8.0.1:53" {
		t.Errorf("after the move, dotted names go to %s, want the new primary's 10.8.0.1:53", got)
	}
	if input, effective := o.reassert(); !input || !effective {
		t.Errorf("the reassert after a move reported input=%v effective=%v, want both: Y's keys were not ours yet", input, effective)
	}
	for _, k := range []string{"State:/Network/Service/Y/DNS", "Setup:/Network/Service/Y/DNS"} {
		if got := st.servers(k); len(got) != 1 || got[0] != dnsIP {
			t.Errorf("after the move, %s holds %v, want plug's %s", k, got, dnsIP)
		}
	}

	// restore: every key back to what it held, the ones plug created gone, the
	// files back. The store must hold nothing of plug's.
	o.restore()
	want := map[string][]string{
		"State:/Network/Service/X/DNS": {"10.0.0.1"},
		"State:/Network/Service/Y/DNS": {"10.8.0.1"},
		"State:/Network/Global/DNS":    {"10.0.0.1"},
	}
	if got := strings.Join(st.keys(), " "); got != "State:/Network/Global/DNS State:/Network/Service/X/DNS State:/Network/Service/Y/DNS" {
		t.Errorf("after restore the store holds %s, want only the three keys the machine had", got)
	}
	for k, servers := range want {
		if got := st.servers(k); strings.Join(got, ",") != strings.Join(servers, ",") {
			t.Errorf("after restore %s holds %v, want %v", k, got, servers)
		}
	}
	for _, k := range st.keys() {
		if poisonedByPlug(st.servers(k)) {
			t.Errorf("after restore %s still points at a plug resolver", k)
		}
	}
	if _, err := os.Stat(resolverPath); err == nil {
		t.Error("the scoped resolver file survived the restore")
	}
	if b, _ := os.ReadFile(resolvConf); string(b) != "nameserver 10.0.0.1\n" {
		t.Errorf("resolv.conf after restore: %q", b)
	}
}

// A leftover of plug's on the service being taken over is not the state to
// return to: the restore must REMOVE the key rather than hand the breakage on,
// at startup and on a move alike.
func TestALeftoverOnTheNewPrimaryIsDroppedNotRestored(t *testing.T) {
	dir := t.TempDir()
	savedResolv, savedResolver := resolvConf, resolverPath
	t.Cleanup(func() { resolvConf, resolverPath = savedResolv, savedResolver })
	resolvConf = filepath.Join(dir, "resolv.conf")
	resolverPath = filepath.Join(dir, "resolver", searchSuffix)

	const dnsIP = "198.18.0.53"
	st := newDynStore()
	st.shown["State:/Network/Service/X/DNS"] = dict("ServerAddresses = 10.0.0.1")
	st.shown["Setup:/Network/Service/X/DNS"] = dict("ServerAddresses = 198.18.0.53") // a dead session's
	st.shown["State:/Network/Global/DNS"] = dict("ServerAddresses = 198.18.0.53")    // same
	st.install(t)

	o := newMacDNSOverride("X", dnsIP, newUpstream(nil), logfn(func(string, ...any) {}))
	o.apply()
	o.restore()
	for _, k := range []string{"Setup:/Network/Service/X/DNS", "State:/Network/Global/DNS"} {
		if _, ok := st.shown[k]; ok {
			t.Errorf("%s was restored to a plug resolver: a previous session's wreckage handed on", k)
		}
	}
	if got := st.servers("State:/Network/Service/X/DNS"); len(got) != 1 || got[0] != "10.0.0.1" {
		t.Errorf("X holds %v after restore, want its own 10.0.0.1", got)
	}
}

// The watchdog reads the store every three seconds for as long as the daemon
// lives: its three keys and every service's resolver, a VPN's scoped one
// included. In one scutil for the keys and one for the resolvers, whatever the
// number of services, where a fork per key made six to eight a tick.
func TestAWatchdogTickReadsTheStoreInTwoRuns(t *testing.T) {
	dir := t.TempDir()
	savedResolv, savedResolver := resolvConf, resolverPath
	t.Cleanup(func() { resolvConf, resolverPath = savedResolv, savedResolver })
	resolvConf = filepath.Join(dir, "resolv.conf")
	resolverPath = filepath.Join(dir, "resolver", searchSuffix)
	write(t, resolvConf, "nameserver 10.0.0.1\n")

	const dnsIP = "198.18.0.53"
	st := newDynStore()
	st.shown["State:/Network/Service/X/DNS"] = dict("ServerAddresses = 10.0.0.1")
	st.shown["State:/Network/Global/DNS"] = dict("ServerAddresses = 10.0.0.1")
	st.shown["State:/Network/Service/V1/DNS"] = dict("ServerAddresses = 10.8.0.1", "SupplementalMatchDomains = corp.example")
	st.shown["State:/Network/Service/V2/DNS"] = dict("ServerAddresses = 10.9.0.1", "SupplementalMatchDomains = lab.example")
	st.install(t)
	up := newUpstream(nil)
	o := newMacDNSOverride("X", dnsIP, up, logfn(func(string, ...any) {}))
	o.apply()

	st.calls = 0
	if input, effective := o.reassert(); input || effective {
		t.Fatalf("reassert after apply reported input=%v effective=%v: nothing moved", input, effective)
	}
	if st.calls != 2 {
		t.Errorf("a tick ran scutil %d times, want 2", st.calls)
	}
	// And what it read in those two is what it used: both VPNs' domains go to
	// their own resolvers.
	up.mu.RLock()
	scoped := up.scoped
	up.mu.RUnlock()
	for domain, want := range map[string]string{"corp.example": "10.8.0.1:53", "lab.example": "10.9.0.1:53"} {
		got := ""
		for _, sc := range scoped {
			if sc.domain == domain {
				got = strings.Join(sc.addrs, ",")
			}
		}
		if got != want {
			t.Errorf("%s is forwarded to %q, want %s", domain, got, want)
		}
	}
}

// scutilBatch is only as good as its split: each command's output must be what
// that command printed run on its own. Checked against the machine's scutil,
// which answers reads without root.
func TestABatchReadsWhatEachCommandReadsAlone(t *testing.T) {
	cmds := []string{"show State:/Network/Global/IPv4", "show State:/Network/Plug/NoSuchKey", listDNSKeysCmd, "show State:/Network/Global/DNS"}
	got := scutilBatch(cmds...)
	for i, c := range cmds {
		alone, err := scutil(c + "\nquit\n")
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if strings.TrimSpace(got[i]) != strings.TrimSpace(alone) {
			t.Errorf("%s in a batch printed\n%s\nalone\n%s", c, got[i], alone)
		}
	}
	if !strings.Contains(got[1], "No such key") {
		t.Errorf("a missing key in a batch printed %q", got[1])
	}
}
