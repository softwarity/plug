//go:build linux

package tun

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

// probeResolverAddr is where the fake VPN's resolver answers. TEST-NET-2
// (RFC 5737): assigned to nobody and routed nowhere, so putting it on lo cannot
// shadow a network this machine is on — and it is deliberately NOT a loopback
// address, because a real VPN never announces one.
const probeResolverAddr = "198.51.100.53"

// resolvedStub is the one nameserver /etc/resolv.conf lists when
// systemd-resolved is the machine's resolver (Ubuntu, Fedora, recent Debian).
// plug forwards dotted names to whatever that file lists, so on such a machine
// it forwards them to the stub, and the stub applies the routing domains a VPN
// declares on its link (`~corp.example`) by itself: split DNS needs no code of
// plug's there. The scoped half of the probe proves exactly that.
const resolvedStub = "127.0.0.53"

// probeLink is the link the fake VPN declares its domain on, the way a VPN
// client declares it on its tunnel interface. A dummy link of its own, because
// systemd-resolved ignores lo for per-link servers, and created for the probe
// only, so no link of the machine's gets a DNS setting it did not have.
const probeLink = "plugvpn0"

// newVPNRig fakes a VPN the way Linux sees one: nameservers appearing in the file
// plug follows. It redirects that file to a temporary one rather than rewriting
// the machine's own — see resolvPath. What that trades away is the assertion that
// the production path is /etc/resolv.conf; the check below buys it back the only
// way that stays safe, by reading the real thing before stepping aside.
//
// Where systemd-resolved is the resolver, the rig also declares a domain of its
// own on its link, through resolvectl: what a corporate client pushes, and what
// the machine's resolv.conf never shows.
func newVPNRig(original []string, _ string, log logfn) (*vpnRig, error) {
	if len(original) == 0 {
		return nil, fmt.Errorf("%s lists no nameserver — there is nothing to come back to, "+
			"so the probe cannot test the VPN going away", resolvFile())
	}
	if got := resolvNameservers(); len(got) == 0 {
		return nil, fmt.Errorf("%s is unreadable or lists no nameserver — that is the file plug "+
			"follows on Linux, so the probe would be testing a path production does not use", resolvFile())
	}

	resolvectl, scoped := resolvedScopes(original)
	if !scoped {
		log.f("vpn probe: the machine's resolver is %v, not systemd-resolved's stub with resolvectl: "+
			"no per-link domain to declare here", original)
	}

	// The address has to exist here before the fake resolver can bind port 53 on
	// it. /32, local delivery, no route to anywhere, nothing else affected: on lo
	// without systemd-resolved, on the probe's own link with it, since the
	// servers resolved asks for a link's domain are reached over that link.
	host := "lo"
	dropLink := func() {}
	if scoped {
		if err := addDummyLink(probeLink); err != nil {
			return nil, fmt.Errorf("create the fake VPN's link %s: %w", probeLink, err)
		}
		dropLink = func() {
			_ = exec.Command(resolvectl, "revert", probeLink).Run()
			if err := delLink(probeLink); err != nil {
				log.f("vpn probe: WARNING link %s left behind: %v", probeLink, err)
			}
		}
		if err := linkUp(probeLink); err != nil {
			dropLink()
			return nil, err
		}
		host = probeLink
	}
	probeAddr := netip.PrefixFrom(netip.MustParseAddr(probeResolverAddr), 32)
	if err := addrAdd(host, probeAddr); err != nil {
		dropLink()
		return nil, fmt.Errorf("add %s on %s: %w", probeResolverAddr, host, err)
	}
	dropAddr := func() {
		if err := addrDel(host, probeAddr); err != nil {
			log.f("vpn probe: WARNING %s left on %s: %v", probeResolverAddr, host, err)
		}
		dropLink()
	}

	f, err := os.CreateTemp("", "plug-vpnprobe-resolv-*.conf")
	if err != nil {
		dropAddr()
		return nil, fmt.Errorf("temp resolv.conf: %w", err)
	}
	tmp := f.Name()
	_ = f.Close()
	write := func(servers []string) error {
		var b strings.Builder
		for _, s := range servers {
			b.WriteString("nameserver " + s + "\n")
		}
		return os.WriteFile(tmp, []byte(b.String()), 0o644)
	}
	// Start from what the machine really has, so redirecting the path is a no-op
	// the watcher does not even notice: the only change it will ever see is the
	// one the probe makes on purpose.
	if err := write(original); err != nil {
		dropAddr()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("seed temp resolv.conf: %w", err)
	}
	real := setResolvFile(tmp)

	rig := &vpnRig{
		resolverAddr: probeResolverAddr,
		announce:     func() error { return write([]string{probeResolverAddr}) },
		restore:      func() error { return write(original) },
		close: func() {
			setResolvFile(real) // production path first — nothing below may leave it redirected
			_ = os.Remove(tmp)
			dropAddr()
		},
	}
	if scoped {
		// The scope is tested with the machine's own servers back, which here
		// means the stub: plug forwards the scoped name to it like any other,
		// and only the stub's routing can take it to the VPN's resolver.
		rig.scopeBehindStub = true
		rig.scope = func() error {
			if err := runResolvectl(resolvectl, "dns", probeLink, probeResolverAddr); err != nil {
				return err
			}
			return runResolvectl(resolvectl, "domain", probeLink, "~"+scopeDomain)
		}
		rig.unscope = func() error {
			if err := runResolvectl(resolvectl, "revert", probeLink); err != nil {
				return err
			}
			// A name the scope answered stays in the stub's cache for its TTL;
			// the probe asserts it is gone, so ask for it not to be there.
			return runResolvectl(resolvectl, "flush-caches")
		}
	}
	return rig, nil
}

// resolvedScopes reports whether the machine's resolver applies per-link
// domains plug can rely on, and the resolvectl that declares them: the file
// plug follows must name the stub first (a machine where systemd-resolved runs
// but resolv.conf lists the upstream servers themselves bypasses it, and has no
// split DNS for plug to keep), and resolvectl must be there to declare a scope.
func resolvedScopes(original []string) (string, bool) {
	if len(original) == 0 || original[0] != resolvedStub {
		return "", false
	}
	path, err := exec.LookPath("resolvectl")
	if err != nil {
		return "", false
	}
	return path, true
}

func runResolvectl(resolvectl string, args ...string) error {
	if out, err := exec.Command(resolvectl, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("resolvectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// addDummyLink creates a dummy link named name (`ip link add <name> type
// dummy`), over the same rtnetlink as the TUN's own setup. A link of that name
// left by a probe that died is removed first: it is the probe's, by name.
func addDummyLink(name string) error {
	msg := make([]byte, unix.SizeofIfInfomsg)
	msg[0] = unix.AF_UNSPEC
	msg = append(msg, rtattr(unix.IFLA_IFNAME, append([]byte(name), 0))...)
	msg = append(msg, rtattr(unix.IFLA_LINKINFO, rtattr(unix.IFLA_INFO_KIND, []byte("dummy")))...)
	err := rtnetlink(unix.RTM_NEWLINK, unix.NLM_F_CREATE|unix.NLM_F_EXCL, msg)
	if errors.Is(err, unix.EEXIST) {
		if derr := delLink(name); derr != nil {
			return fmt.Errorf("a link %s is already there and cannot be removed: %w", name, derr)
		}
		err = rtnetlink(unix.RTM_NEWLINK, unix.NLM_F_CREATE|unix.NLM_F_EXCL, msg)
	}
	if err != nil {
		return fmt.Errorf("add dummy link %s: %w", name, err)
	}
	return nil
}

// delLink removes the link name (`ip link del <name>`), its addresses with it.
func delLink(name string) error {
	idx, err := ifIndex(name)
	if err != nil {
		return err
	}
	msg := make([]byte, unix.SizeofIfInfomsg)
	msg[0] = unix.AF_UNSPEC
	binary.NativeEndian.PutUint32(msg[4:8], uint32(idx))
	if err := rtnetlink(unix.RTM_DELLINK, 0, msg); err != nil {
		return fmt.Errorf("delete link %s: %w", name, err)
	}
	return nil
}
