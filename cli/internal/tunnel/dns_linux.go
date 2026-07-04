//go:build linux

package tunnel

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
)

const resolvConf = "/etc/resolv.conf"
const dnsBackup = "/var/run/plugd-resolv.backup"

// setupDNS starts the split resolver on 127.0.0.1:53 and points /etc/resolv.conf
// at it, returning a restore function. (systemd-resolved setups may need
// per-link config; resolv.conf is the portable baseline.)
func setupDNS(ctx context.Context, tr *Transport, logf Logf) (func(), error) {
	orig, err := os.ReadFile(resolvConf)
	if err != nil {
		return nil, err
	}
	upstreams := parseResolvConf(orig)

	r := NewResolver(tr, upstreams, 0, logf)
	if _, err := r.Serve(ctx, "127.0.0.1:53"); err != nil {
		return nil, fmt.Errorf("resolver listen: %w", err)
	}

	os.WriteFile(dnsBackup, orig, 0o600)
	if err := os.WriteFile(resolvConf, []byte("# plug tunnel\nnameserver 127.0.0.1\n"), 0o644); err != nil {
		return nil, err
	}
	logf("split DNS active — %d upstream(s)", len(upstreams))

	return func() {
		os.WriteFile(resolvConf, orig, 0o644)
		os.Remove(dnsBackup)
		logf("system DNS restored")
	}, nil
}

func parseResolvConf(data []byte) []string {
	var ups []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "nameserver" && f[1] != "127.0.0.1" {
			ups = append(ups, net.JoinHostPort(f[1], "53"))
		}
	}
	return ups
}

// RestoreLeftoverDNS restores resolv.conf from a backup left by a crashed
// session. Called at daemon startup.
func RestoreLeftoverDNS() {
	data, err := os.ReadFile(dnsBackup)
	if err != nil {
		return
	}
	os.WriteFile(resolvConf, data, 0o644)
	os.Remove(dnsBackup)
}
