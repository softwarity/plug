package main

import (
	"context"
	"encoding/pem"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// inviteAgent speaks the agent's side of admission by approval, as the agent's
// admission.go and sshserver.go do: the key built into plug is told to bring
// its own, an unknown key that signed is asked its name and recorded, the
// first one of an empty roster is welcomed in. The words are the protocol's,
// so a change on either side shows here.
type inviteAgent struct {
	mu       sync.Mutex
	shared   ssh.PublicKey
	members  map[string]string // fingerprint -> name
	requests map[string]string
	conns    []*ssh.ServerConn
}

// revoke makes fp no longer a member, someone else being one, and cuts every
// connection, the way the real agent ends a revoked member's sessions.
func (a *inviteAgent) revoke(fp string) {
	a.mu.Lock()
	delete(a.members, fp)
	a.members["SHA256:someone-else"] = "alice@laptop"
	conns := a.conns
	a.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

func startInviteAgent(t *testing.T, members map[string]string) (*inviteAgent, string, string) {
	t.Helper()
	embedded, err := ssh.ParsePrivateKey(embeddedKey)
	if err != nil {
		t.Fatal(err)
	}
	a := &inviteAgent{shared: embedded.PublicKey(), members: members, requests: map[string]string{}}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			a.mu.Lock()
			defer a.mu.Unlock()
			if _, ok := a.members[ssh.FingerprintSHA256(key)]; ok {
				return &ssh.Permissions{}, nil
			}
			return &ssh.Permissions{Extensions: map[string]string{"admit": "1"}}, nil
		},
		VerifiedPublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey, p *ssh.Permissions, _ string) (*ssh.Permissions, error) {
			if p.Extensions["admit"] != "1" {
				return p, nil
			}
			return nil, &ssh.PartialSuccessError{Next: ssh.ServerAuthCallbacks{
				KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, ask ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
					ans, err := ask("plug-admission", "ask-name", []string{"name: "}, []bool{true})
					if err != nil || len(ans) != 1 {
						return nil, err
					}
					fp := ssh.FingerprintSHA256(key)
					a.mu.Lock()
					defer a.mu.Unlock()
					switch {
					case string(key.Marshal()) == string(a.shared.Marshal()):
						_, _ = ask("plug-admission", "own-key", nil, nil)
					case len(a.members) == 0:
						a.members[fp] = ans[0]
						_, _ = ask("plug-admission", "welcome", nil, nil)
						return &ssh.Permissions{}, nil
					default:
						a.requests[fp] = ans[0]
						var names []string
						for _, n := range a.members {
							names = append(names, n)
						}
						_, _ = ask("plug-admission", "pending fp="+strings.TrimPrefix(fp, "SHA256:")[:12]+" members="+strings.Join(names, ","), nil, nil)
					}
					return nil, errNotAMember
				},
			}}
		},
	}
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
				a.mu.Lock()
				a.conns = append(a.conns, sc)
				a.mu.Unlock()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					_ = nc.Reject(ssh.Prohibited, "test agent")
				}
			}(c)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	return a, host, port
}

type notAMember struct{}

func (notAMember) Error() string { return "not a member" }

var errNotAMember = notAMember{}

func pemKey(t *testing.T) []byte {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(mustGenerateKey(t), "")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

// A newcomer's plug, refused for presenting the shared key, makes the profile
// a key of its own, presents it, and is told what to ask of whom, with the
// command that names this profile.
func TestANewcomerGetsAKeyOfItsOwnAndTheLineToSend(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "demo")
	was := tunnel.MachineName
	tunnel.MachineName = func() string { return "alice@laptop" }
	t.Cleanup(func() { tunnel.MachineName = was })
	agent, host, port := startInviteAgent(t, map[string]string{"SHA256:someone": "francois"})

	cfg := loadProfile("demo")
	cfg.host, cfg.port = host, port
	_, err := dialAgent(&cfg)
	if err == nil {
		t.Fatal("a key nobody accepted got in")
	}
	priv := profileKeyPath("demo")
	if _, statErr := os.Stat(priv); statErr != nil {
		t.Fatalf("no key was made for the profile: %v", statErr)
	}
	if loadProfile("demo").key != priv {
		t.Errorf("the profile does not name its new key: %q", loadProfile("demo").key)
	}
	agent.mu.Lock()
	var name, fp string
	for f, n := range agent.requests {
		fp, name = f, n
	}
	agent.mu.Unlock()
	if name != "alice@laptop" {
		t.Fatalf("the agent recorded %q, want the machine's name", name)
	}
	msg := err.Error()
	for _, want := range []string{
		"plug -p demo accept " + strings.TrimPrefix(fp, "SHA256:")[:12],
		"plug -p demo requests",
		"Members: francois",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, msg)
		}
	}
}

func TestTheFirstMemberIsLetIn(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "demo")
	_, host, port := startInviteAgent(t, map[string]string{})
	cfg := loadProfile("demo")
	cfg.host, cfg.port = host, port
	tr, err := dialAgent(&cfg)
	if err != nil {
		t.Fatalf("the first key of an empty roster was refused: %v", err)
	}
	defer tr.Close()
	if !tr.Welcomed() {
		t.Error("the first member was not told so")
	}
}

// A member revoked while connected: the agent cuts the connection and refuses
// it back. The transport says so and keeps the refusal, which is what the
// shared datapath daemon reads to drop the tunnel and tell the next plug why;
// without it the session only ever saw its flows fail.
func TestARevokedMembersTransportKnowsItWasRefused(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "demo")
	agent, host, port := startInviteAgent(t, map[string]string{})
	cfg := loadProfile("demo")
	cfg.host, cfg.port = host, port
	tr, err := dialAgent(&cfg)
	if err != nil {
		t.Fatalf("the first member was refused: %v", err)
	}
	defer tr.Close()
	if tr.Refused() != nil {
		t.Fatal("a transport that was let in reports a refusal")
	}
	agent.mu.Lock()
	var fp string
	for k := range agent.members {
		fp = k
	}
	agent.mu.Unlock()
	agent.revoke(fp)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if c, err := tr.DialContext(ctx, "tcp", "anything:80"); err == nil {
		c.Close()
		t.Fatal("a revoked member's transport still opened a connection")
	}
	err = tr.Refused()
	if err == nil {
		t.Fatal("the transport was refused on its reconnect and does not say so")
	}
	if !strings.Contains(err.Error(), "by approval") {
		t.Errorf("the refusal kept is not the agent's: %v", err)
	}
}

func TestTheProfileCrossesTheExecToTheCore(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "demo")
	launcher := loadProfile("demo")
	for _, kv := range coreEnv(launcher) {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "PLUG_CORE") {
			t.Setenv(k, v)
		}
	}
	if got := coreConfigFromEnv().profile; got != "demo" {
		t.Fatalf("the core got profile %q: refused for want of a key, it could not make one", got)
	}
}

func TestAnAgentsListsReadBack(t *testing.T) {
	out := "member\tSHA256:aaa\tfrancois\t-\t2026-10-12T10:00:00Z\t2026-10-12T11:00:00Z\n" +
		"member\tSHA256:bbb\talice@laptop\tfrancois\t2026-10-12T10:30:00Z\t-\n" +
		"garbage line"
	rows := parseRows(out, "member")
	if len(rows) != 2 || rows[1].name != "alice@laptop" || rows[1].by != "francois" || !rows[1].seen.IsZero() {
		t.Fatalf("members read back as %+v", rows)
	}
	reqs := parseRows("request\tSHA256:ccc\tbob@pc\t10.0.0.9\t2026-10-12T10:00:00Z", "request")
	if len(reqs) != 1 || reqs[0].from != "10.0.0.9" || reqs[0].last.IsZero() {
		t.Fatalf("requests read back as %+v", reqs)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	if got := ago(now.Add(-5*time.Minute), now); got != "5 min ago" {
		t.Errorf("ago = %q", got)
	}
}

func TestAChoiceIsNumbersInRange(t *testing.T) {
	for in, want := range map[string][]int{"1": {0}, "1 3": {0, 2}, "3,1,3": {2, 0}} {
		got, ok := parseChoice(in, 3)
		if !ok || len(got) != len(want) {
			t.Errorf("parseChoice(%q) = %v, %v", in, got, ok)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("parseChoice(%q) = %v, want %v", in, got, want)
			}
		}
	}
	for _, bad := range []string{"0", "4", "x", "1 x"} {
		if _, ok := parseChoice(bad, 3); ok {
			t.Errorf("parseChoice(%q) accepted", bad)
		}
	}
}

// The core opens the session's tunnel, and it is told the profile: refused for
// presenting the shared key, it makes the key where `plug keygen` would.
func TestTheSessionsTunnelMakesTheKeyItIsAskedFor(t *testing.T) {
	sandboxHome(t)
	newProfile(t, "demo")
	_, host, port := startInviteAgent(t, map[string]string{})
	cfg := loadProfile("demo")
	cfg.host, cfg.port = host, port
	tr, err := dialTunnel(cfg)
	if err != nil {
		t.Fatalf("the first member's session was refused: %v", err)
	}
	tr.Close()
	if loadProfile("demo").key != profileKeyPath("demo") {
		t.Fatal("the session did not give the profile its key")
	}
}
