//go:build darwin || windows

package tun

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// The name pins, on both OSes that route by attribution. Windows is where they
// carry production traffic (the kernel's SMB client dials the live-mount
// helper, with no ancestry to a launcher), and these tests used to run on macOS
// alone.

// A name pinned by a live client goes to that client's cluster before any
// attribution - the live-mount helper's, dialled by the kernel's SMB client on
// Windows, where no ancestry leads to a launcher. Unpinned, the flow takes the
// ordinary route; a dead pinner's pin is not read.
func TestPinnedNameRoutesToItsCluster(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	ct := NewClusterTransports()
	a, b := loopbackDialer{addr: "A"}, loopbackDialer{addr: "B"}
	ct.Set("cluster-a", a)
	ct.Set("cluster-b", b)
	unreg := RegisterClient("cluster-b", os.Getpid(), "")
	defer unreg()
	PinName("cluster-b", os.Getpid(), "Plug-Mnt-Web-1234abcd")
	defer UnpinNames("cluster-b", os.Getpid())
	pinCache = map[string]pinEntry{}
	d, key, ok := multiDial(ct)(at(40000), "plug-mnt-web-1234abcd")
	if !ok || key != "cluster-b" || d.(loopbackDialer).addr != "B" {
		t.Fatalf("pinned name: %v %q %v", ok, key, d)
	}
	// Not pinned: two clusters, an unattributable port → refused as before.
	pinCache = map[string]pinEntry{}
	if _, _, ok := multiDial(ct)(at(40000), "other"); ok {
		t.Fatal("an unpinned name with two clusters must still be attributed")
	}
	// A dead pinner: its pin is nobody's.
	UnpinNames("cluster-b", os.Getpid())
	PinName("cluster-b", 999999, "plug-mnt-web-1234abcd")
	pinCache = map[string]pinEntry{}
	if k, ok := pinnedCluster("plug-mnt-web-1234abcd"); ok {
		t.Fatalf("a dead client's pin was honoured: %q", k)
	}
}

// The per-name pin verdicts are taken for every name any process looks up,
// before any account check, and were kept for ever. At the bound the stale
// ones go; a verdict recomputed is a directory scan, a map that only grows is
// a leak in a daemon that runs for weeks.
func TestStalePinVerdictsAreForgottenAtTheBound(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()
	saved := pinCache
	defer func() { pinCache = saved }()

	stale := time.Now().Add(-2 * pinTTL)
	pinCache = map[string]pinEntry{}
	for i := 0; i < pinCacheMax; i++ {
		pinCache["name-"+strconv.Itoa(i)] = pinEntry{at: stale}
	}
	pinnedCluster("one-more")
	if n := len(pinCache); n != 1 {
		t.Errorf("%d verdicts kept past the bound with every TTL elapsed, want the one just taken", n)
	}
	// Fresh verdicts that fill the bound by themselves are dropped whole rather
	// than kept.
	for i := 0; i < pinCacheMax; i++ {
		pinCache["fresh-"+strconv.Itoa(i)] = pinEntry{at: time.Now()}
	}
	pinnedCluster("over")
	if n := len(pinCache); n > pinCacheMax {
		t.Errorf("%d verdicts kept, more than the bound %d", n, pinCacheMax)
	}
}
