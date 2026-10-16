//go:build darwin || windows

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// reconcile is the loop that decides which tunnels the shared daemon holds: one
// per cluster with a live client, opened with that client's key, kept through a
// grace after the last one leaves, never dialled twice at once, and not
// re-dialled three times a second after the agent said no. These tests run it
// end to end against a registry of their own and an agent on loopback, with the
// dial counted and, where a rule needs it, held.

// openAgent is an SSH server that lets anyone in and opens nothing: enough for
// tunnel.Dial to return a live transport, which reach opens.
func openAgent(t *testing.T) (host, port string, reach func() (*tunnel.Transport, error)) {
	t.Helper()
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	hk, err := ssh.ParsePrivateKey(pemKey(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.AddHostKey(hk)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					_ = nc.Reject(ssh.Prohibited, "test agent")
				}
			}(c)
		}
	}()
	host, port, _ = net.SplitHostPort(ln.Addr().String())
	keys := [][]byte{pemKey(t)}
	return host, port, func() (*tunnel.Transport, error) { return tunnel.Dial(host, port, sshUser, keys, "", nil) }
}

// freshDaemon gives a test the daemon's state as a new daemon has it: an empty
// registry in a directory of its own, no refusal remembered, no tick before
// this one, and a dial that the test supplies. Everything is put back after.
func freshDaemon(t *testing.T, dial func(config) (*tunnel.Transport, error)) (*tun.ClusterTransports, map[string]*tunnel.Transport) {
	t.Helper()
	t.Cleanup(tun.UseStateDir(t.TempDir()))
	oldDial, oldFlush := dialClusterTunnel, flushSystemDNS
	oldRefused, oldRetry, oldLast, oldIdle := authRefused, authRetryIn, activeLastTick, tunnelIdleSince
	dialClusterTunnel, flushSystemDNS = dial, func() {}
	authRefused, authRetryIn = map[string]time.Time{}, map[string]time.Duration{}
	activeLastTick, tunnelIdleSince = map[string]bool{}, map[string]time.Time{}
	ct, tunnels := tun.NewClusterTransports(), map[string]*tunnel.Transport{}
	t.Cleanup(func() {
		settleDials(t)
		for k := range tunnels {
			dropTunnel(ct, tunnels, k)
		}
		dialClusterTunnel, flushSystemDNS = oldDial, oldFlush
		authRefused, authRetryIn, activeLastTick, tunnelIdleSince = oldRefused, oldRetry, oldLast, oldIdle
	})
	return ct, tunnels
}

// settleDials waits for every dial a test started to return, and throws away
// its result: left in dialOut, the next test's first reconcile would install it.
func settleDials(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		dials.mu.Lock()
		n := len(dials.in)
		dials.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d dials still in flight after the test", n)
		}
	}
	for {
		select {
		case r := <-dialOut:
			if r.tr != nil {
				r.tr.Close()
			}
		default:
			return
		}
	}
}

func dialsOf(calls *sync.Map, key string) int32 {
	n, ok := calls.Load(key)
	if !ok {
		return 0
	}
	return n.(*atomic.Int32).Load()
}

// tickUntil runs the ticker's reconcile until cond holds, the way the daemon
// would three times a second, and says whether it did in time.
func tickUntil(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport, cond func() bool) bool {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		reconcile(ct, tunnels, false)
		if cond() {
			return true
		}
	}
	return false
}

func TestATunnelOpensWithTheClientsKeyAndOutlivesItThroughTheGrace(t *testing.T) {
	host, port, reach := openAgent(t)
	var asked config
	ct, tunnels := freshDaemon(t, func(cfg config) (*tunnel.Transport, error) {
		asked = cfg
		return reach()
	})
	key := net.JoinHostPort(host, port)
	// The key sits in the account's profile, as a profile key does. Resolved,
	// because a Windows runner names its temp directory in 8.3 form
	// (C:\Users\RUNNER~1\...), which the guard on whose key it is
	// (keyOwnerError) compares as text against the profile's long name.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte("not read: the dial is the test's"), 0o600); err != nil {
		t.Fatal(err)
	}
	unregister := tun.RegisterClient(key, os.Getpid(), keyFile)

	reconcileOnce(ct, tunnels)
	if tunnels[key] == nil || !ct.Has(key) {
		t.Fatal("a cluster with a live client has no tunnel after the first reconcile")
	}
	if !tun.ClusterReady(key) {
		t.Fatal("the tunnel is up and the launcher waiting on it is not told so")
	}
	if asked.host != host || asked.port != port || asked.key != keyFile {
		t.Fatalf("the daemon dialled %s:%s with key %q, want %s with the key the client registered, %q",
			asked.host, asked.port, asked.key, key, keyFile)
	}

	// The client leaves: the tunnel stays, so the next plug of the same
	// cluster finds it instead of paying a dial.
	unregister()
	reconcile(ct, tunnels, false)
	reconcile(ct, tunnels, false)
	if tunnels[key] == nil || !tun.ClusterReady(key) {
		t.Fatal("the tunnel was dropped the moment its last client left, before its grace")
	}

	// The grace runs out: the tunnel goes, and so does the ready marker, or a
	// launcher would start on a tunnel that is no longer there.
	tunnelIdleSince[key] = time.Now().Add(-tunnelGrace - time.Second)
	reconcile(ct, tunnels, false)
	if tunnels[key] != nil || ct.Has(key) {
		t.Fatal("an idle tunnel outlived its grace")
	}
	if tun.ClusterReady(key) {
		t.Fatal("the tunnel is down and its cluster is still marked ready")
	}
}

func TestARefusedKeyIsTakenAtItsWordUntilTheClusterComesBack(t *testing.T) {
	var dials atomic.Int32
	ct, tunnels := freshDaemon(t, func(cfg config) (*tunnel.Transport, error) {
		dials.Add(1)
		return nil, &tunnel.AuthFailure{Addr: net.JoinHostPort(cfg.host, cfg.port), Err: errors.New("no"), Offered: []string{"SHA256:test"}}
	})
	key := "127.0.0.1:1"
	unregister := tun.RegisterClient(key, os.Getpid(), "")

	reconcileOnce(ct, tunnels)
	if dials.Load() != 1 {
		t.Fatalf("the first reconcile dialled %d times, want 1", dials.Load())
	}
	if !strings.Contains(tun.ClusterError(key), "refused every key") {
		t.Fatalf("the launcher waiting on the cluster is not told why: %q", tun.ClusterError(key))
	}
	if tunnels[key] != nil || tun.ClusterReady(key) {
		t.Fatal("a refused cluster has a tunnel")
	}

	// The same client, tick after tick: the agent's answer stands.
	for range 10 {
		reconcile(ct, tunnels, false)
	}
	if dials.Load() != 1 {
		t.Fatalf("a refusal was re-asked %d times within its minute", dials.Load()-1)
	}

	// The cluster loses its last client for a tick and someone runs plug for
	// it again: that is the newcomer whose key was just accepted, and it is
	// asked again at once rather than told the old answer.
	unregister()
	reconcile(ct, tunnels, false)
	defer tun.RegisterClient(key, os.Getpid(), "")()
	if !tickUntil(ct, tunnels, func() bool { return dials.Load() == 2 }) {
		t.Fatalf("a cluster that came back was not asked again (%d dials)", dials.Load())
	}
}

func TestADialThatHangsHoldsNeitherTheOtherClusterNorASecondDial(t *testing.T) {
	host, port, reach := openAgent(t)
	var calls sync.Map // cluster -> dials started
	release := make(chan struct{})
	// Two cluster keys for the one agent: what tells them apart is the key,
	// and the dial reaches the agent whatever it is asked for.
	slow, fast := net.JoinHostPort("localhost", port), net.JoinHostPort(host, port)
	ct, tunnels := freshDaemon(t, func(cfg config) (*tunnel.Transport, error) {
		asked := net.JoinHostPort(cfg.host, cfg.port)
		n, _ := calls.LoadOrStore(asked, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		if asked == slow {
			<-release
		}
		return reach()
	})
	// The slow dial is let go however the test ends, or it outlives it.
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	defer tun.RegisterClient(slow, os.Getpid(), "")()
	defer tun.RegisterClient(fast, os.Getpid(), "")()

	if !tickUntil(ct, tunnels, func() bool { return tunnels[fast] != nil }) {
		t.Fatal("a cluster waited behind another cluster's dial")
	}
	if tunnels[slow] != nil {
		t.Fatal("the held dial finished")
	}
	for range 10 {
		reconcile(ct, tunnels, false)
	}
	if n := dialsOf(&calls, slow); n != 1 {
		t.Fatalf("ticks during one dial in flight started %d dials, want 1", n)
	}

	letGo()
	if !tickUntil(ct, tunnels, func() bool { return tunnels[slow] != nil }) {
		t.Fatal("the dial was let go and its tunnel never installed")
	}
	if n := dialsOf(&calls, slow); n != 1 {
		t.Fatalf("the cluster was dialled again after its tunnel came up (%d dials)", n)
	}
	if !tun.ClusterReady(slow) || !tun.ClusterReady(fast) {
		t.Fatal("a tunnel is up and its cluster is not marked ready")
	}
}
