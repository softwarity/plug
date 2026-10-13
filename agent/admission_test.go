package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// memStore is a roster store in memory, failing on demand.
type memStore struct {
	mu      sync.Mutex
	b       []byte
	loadErr error
}

func (m *memStore) where() string { return "memory" }
func (m *memStore) load() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b, m.loadErr
}
func (m *memStore) save(b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.b = append([]byte(nil), b...)
	return nil
}
func (m *memStore) roster(t *testing.T) roster {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var r roster
	if len(m.b) > 0 {
		if err := json.Unmarshal(m.b, &r); err != nil {
			t.Fatalf("roster: %v", err)
		}
	}
	return r
}

// admissionClient is plug's side of the exchange, written the way the CLI's is:
// it answers the name question and keeps what the agent said.
type admissionClient struct {
	name string
	mu   sync.Mutex
	said []string
}

func (a *admissionClient) auth() ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(name, instruction string, questions []string, _ []bool) ([]string, error) {
		if name != admissionChallenge {
			return nil, errors.New("not plug's exchange")
		}
		if len(questions) == 1 && instruction == sayAskName {
			return []string{a.name}, nil
		}
		a.mu.Lock()
		a.said = append(a.said, instruction)
		a.mu.Unlock()
		return make([]string, len(questions)), nil
	})
}

func (a *admissionClient) last() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.said) == 0 {
		return ""
	}
	return a.said[len(a.said)-1]
}

func newTestInviteHost(t *testing.T, store rosterStore, shared ...ssh.PublicKey) *inviteHost {
	t.Helper()
	h, err := newInviteHost(shared, t.TempDir()+"/host_key", store, func(string, ...any) {})
	if err != nil {
		t.Fatalf("newInviteHost: %v", err)
	}
	return h
}

// run executes a command over an established client and returns its output.
func run(t *testing.T, cl *ssh.Client, cmd string) string {
	t.Helper()
	s, err := cl.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer s.Close()
	out, _ := s.CombinedOutput(cmd)
	return strings.TrimSpace(string(out))
}

// join connects as the tunnel user with one key, going through the admission
// exchange when the agent asks for it.
func join(t *testing.T, addr string, key ssh.Signer, who *admissionClient) (*ssh.Client, error) {
	t.Helper()
	return dial(t, addr, tunnelUser, ssh.PublicKeys(key), who.auth())
}

func TestTheFirstKeyBecomesTheFirstMember(t *testing.T) {
	store := &memStore{}
	addr := startServer(t, newTestInviteHost(t, store))
	key, _ := newTestKey(t)
	me := &admissionClient{name: "Francois@Mac!"}

	cl, err := join(t, addr, key, me)
	if err != nil {
		t.Fatalf("the first key of an empty roster was refused: %v", err)
	}
	defer cl.Close()
	if me.last() != sayWelcome {
		t.Errorf("the first member was told %q, want %q", me.last(), sayWelcome)
	}
	r := store.roster(t)
	if len(r.Members) != 1 || r.Members[0].Name != "francois@mac" || r.Members[0].By != "" {
		t.Fatalf("roster after the first key: %+v", r.Members)
	}
	if got := run(t, cl, "info"); !strings.HasPrefix(got, "plug:info") {
		t.Errorf("a member's command did not reach the verbs: %q", got)
	}
}

func TestAnUnknownKeyIsRecordedAndRefused(t *testing.T) {
	store := &memStore{}
	addr := startServer(t, newTestInviteHost(t, store))
	first, _ := newTestKey(t)
	cl, err := join(t, addr, first, &admissionClient{name: "francois@mac"})
	if err != nil {
		t.Fatal(err)
	}
	cl.Close()

	other, otherPub := newTestKey(t)
	alice := &admissionClient{name: "alice@laptop"}
	if cl, err := join(t, addr, other, alice); err == nil {
		cl.Close()
		t.Fatal("a key nobody accepted got in")
	}
	want := fmt.Sprintf("%s fp=%s members=francois@mac", sayPending, shortFP(ssh.FingerprintSHA256(otherPub)))
	if alice.last() != want {
		t.Errorf("the newcomer was told %q, want %q", alice.last(), want)
	}
	r := store.roster(t)
	if len(r.Requests) != 1 || r.Requests[0].Name != "alice@laptop" || r.Requests[0].From != "127.0.0.1" {
		t.Fatalf("requests: %+v", r.Requests)
	}

	// Again from the same machine: the same request, attempted again.
	first0 := r.Requests[0].First
	if cl, err := join(t, addr, other, alice); err == nil {
		cl.Close()
	}
	r = store.roster(t)
	if len(r.Requests) != 1 || !r.Requests[0].First.Equal(first0) {
		t.Errorf("a second attempt with the same key is not the same request: %+v", r.Requests)
	}
}

// signOnly offers a key and cannot sign with it: the client asks "would this
// key do?", which is all a stranger holding only somebody's PUBLIC key can do.
type signOnly struct{ ssh.Signer }

func (signOnly) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	return nil, errors.New("no private half")
}

func TestAKeyOnlyOfferedIsNeverRecorded(t *testing.T) {
	store := &memStore{}
	h := newTestInviteHost(t, store)
	addr := startServer(t, h)
	first, _ := newTestKey(t)
	if cl, err := join(t, addr, first, &admissionClient{name: "francois"}); err == nil {
		cl.Close()
	}

	colleague, _ := newTestKey(t)
	if cl, err := join(t, addr, signOnly{colleague}, &admissionClient{name: "mallory"}); err == nil {
		cl.Close()
		t.Fatal("a key that never signed got in")
	}
	if r := store.roster(t); len(r.Requests) != 0 || len(r.Members) != 1 {
		t.Fatalf("an offered key was recorded without proof of possession: %+v", r)
	}

	// And on an EMPTY roster it must not become the first member either.
	empty := &memStore{}
	addr2 := startServer(t, newTestInviteHost(t, empty))
	if cl, err := join(t, addr2, signOnly{colleague}, &admissionClient{name: "mallory"}); err == nil {
		cl.Close()
		t.Fatal("a key that never signed became the first member")
	}
	if r := empty.roster(t); len(r.Members) != 0 {
		t.Fatalf("first member taken without proof of possession: %+v", r.Members)
	}
}

func TestTheSharedKeyIsToldToMakeItsOwn(t *testing.T) {
	store := &memStore{}
	shared, sharedPub := newTestKey(t)
	addr := startServer(t, newTestInviteHost(t, store, sharedPub))
	someone := &admissionClient{name: "x"}
	if cl, err := join(t, addr, shared, someone); err == nil {
		cl.Close()
		t.Fatal("the key built into plug got into an invite agent")
	}
	if someone.last() != sayOwnKey {
		t.Errorf("told %q, want %q", someone.last(), sayOwnKey)
	}
	if r := store.roster(t); len(r.Members)+len(r.Requests) != 0 {
		t.Fatalf("the shared key was recorded: %+v", r)
	}
}

func TestAMemberAcceptsARequestAndRevokesAMember(t *testing.T) {
	store := &memStore{}
	addr := startServer(t, newTestInviteHost(t, store))
	fra, _ := newTestKey(t)
	admin, err := join(t, addr, fra, &admissionClient{name: "francois"})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	ali, aliPub := newTestKey(t)
	if cl, err := join(t, addr, ali, &admissionClient{name: "alice"}); err == nil {
		cl.Close()
	}
	reqs := run(t, admin, "requests")
	if !strings.HasPrefix(reqs, "request\t"+ssh.FingerprintSHA256(aliPub)+"\talice\t127.0.0.1\t") {
		t.Fatalf("requests answered %q", reqs)
	}

	if got := run(t, admin, "accept "+shortFP(ssh.FingerprintSHA256(aliPub))[:8]); !strings.HasPrefix(got, "error: ") {
		t.Errorf("a key named by 8 characters was accepted: %q", got)
	}
	if got := run(t, admin, "accept "+shortFP(ssh.FingerprintSHA256(aliPub))); !strings.HasPrefix(got, "ok accepted alice ") {
		t.Fatalf("accept answered %q", got)
	}
	alice, err := join(t, addr, ali, &admissionClient{name: "alice"})
	if err != nil {
		t.Fatalf("an accepted key was refused: %v", err)
	}
	r := store.roster(t)
	if len(r.Members) != 2 || r.Members[1].By != "francois" || len(r.Requests) != 0 {
		t.Fatalf("roster after accept: %+v", r)
	}

	// Revoked, alice's open connection ends at once.
	ended := make(chan struct{})
	go func() { _ = alice.Wait(); close(ended) }()
	if got := run(t, admin, "revoke "+ssh.FingerprintSHA256(aliPub)); !strings.HasPrefix(got, "ok revoked alice ") {
		t.Fatalf("revoke answered %q", got)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("a revoked member's connection stayed open")
	}
	if cl, err := join(t, addr, ali, &admissionClient{name: "alice"}); err == nil {
		cl.Close()
		t.Fatal("a revoked key got back in")
	}
}

func TestTheLastMemberCannotBeRevoked(t *testing.T) {
	store := &memStore{}
	addr := startServer(t, newTestInviteHost(t, store))
	key, pub := newTestKey(t)
	cl, err := join(t, addr, key, &admissionClient{name: "francois"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	got := run(t, cl, "revoke "+shortFP(ssh.FingerprintSHA256(pub)))
	if !strings.Contains(got, "last member") {
		t.Fatalf("revoking the last member answered %q", got)
	}
	if r := store.roster(t); len(r.Members) != 1 {
		t.Fatalf("the last member was removed: %+v", r.Members)
	}
}

func TestAnInviteAgentThatCannotReadItsRosterDoesNotStart(t *testing.T) {
	_, err := newInviteHost(nil, t.TempDir()+"/k", &memStore{loadErr: errors.New("forbidden")}, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "cannot read its members") {
		t.Fatalf("an unreadable roster was accepted: %v", err)
	}
	if _, err := newInviteHost(nil, t.TempDir()+"/k", &memStore{b: []byte("{not json")}, func(string, ...any) {}); err == nil {
		t.Fatal("a roster that does not parse was accepted")
	}
}

func TestRequestsAreForgottenAfterAWeekAndCapped(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	var r roster
	r.Requests = append(r.Requests, rosterRequest{Name: "stale", Fingerprint: "SHA256:stale", Last: now.Add(-requestLife - time.Minute)})
	for i := 0; i < maxRequests+5; i++ {
		r.Requests = append(r.Requests, rosterRequest{Name: fmt.Sprint(i), Fingerprint: fmt.Sprint("SHA256:", i), Last: now.Add(-time.Duration(i) * time.Minute)})
	}
	r.prune(now)
	if len(r.Requests) != maxRequests {
		t.Fatalf("%d requests kept, want %d", len(r.Requests), maxRequests)
	}
	for _, q := range r.Requests {
		if q.Name == "stale" {
			t.Fatal("a request a week old was kept")
		}
	}
	if r.Requests[0].Name != "0" || r.Requests[maxRequests-1].Name != fmt.Sprint(maxRequests-1) {
		t.Errorf("the most recent attempts were not the ones kept: first %q, last %q", r.Requests[0].Name, r.Requests[maxRequests-1].Name)
	}
}

func TestAKeyIsNamedByItsFingerprintOrEnoughOfIt(t *testing.T) {
	fps := []string{"SHA256:abcdefghijklmnopq", "SHA256:abcdefghijklXYZ", "SHA256:zzzzzzzzzzzzzz"}
	at := func(i int) string { return fps[i] }
	for _, c := range []struct {
		arg  string
		want int
		err  string
	}{
		{"zzzzzzzzzzzz", 2, ""},
		{"SHA256:zzzzzzzzzzzzzz", 2, ""},
		{"abcdefghijklm", 0, ""},
		{"abcdefghijkl", 0, "more than one"},
		{"zzzzzzzz", 0, "too short"},
		{"ZZZZZZZZZZZZ", 0, "no key"},
	} {
		i, err := pick(c.arg, len(fps), at)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("pick(%q): err %v, want %q", c.arg, err, c.err)
			}
			continue
		}
		if err != nil || i != c.want {
			t.Errorf("pick(%q) = %d, %v; want %d", c.arg, i, err, c.want)
		}
	}
}

func TestAMachineNameIsReducedToWhatAListCanShow(t *testing.T) {
	for in, want := range map[string]string{
		"Alice@Laptop":           "alice@laptop",
		"bob smith@host;rm -rf":  "bobsmith@hostrm-rf",
		"":                       "unnamed",
		"\x1b[31m":               "31m",
		strings.Repeat("a", 100): strings.Repeat("a", 64),
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnAgentWithoutARosterSaysWhyItHasNone(t *testing.T) {
	t.Setenv(admissionModeEnv, "open")
	if got, _ := runVerb([]string{"members"}); !strings.Contains(got, admissionEnv+"=invite") {
		t.Errorf("an open agent answered %q", got)
	}
	t.Setenv(admissionModeEnv, "gateway")
	if got, _ := runVerb([]string{"accept", "x"}); !strings.Contains(got, "gateway") {
		t.Errorf("a gateway's agent answered %q", got)
	}
}

// Two agents on one swarm keep two rosters. The configs used to be found by a
// label every agent wrote alike, so a staging stack and a production stack on
// the same swarm would have admitted each other's members.
func TestTwoAgentsOnOneSwarmKeepTheirOwnMembers(t *testing.T) {
	fe := newFakeEngine(t)
	fe.setSwarm(true)
	staging := swarmConfigStore{service: "staging_plug"}
	prod := swarmConfigStore{service: "prod_plug"}
	for _, w := range []struct {
		s    swarmConfigStore
		data string
	}{{staging, "s1"}, {prod, "p1"}, {staging, "s2"}} {
		if err := w.s.save([]byte(w.data)); err != nil {
			t.Fatal(err)
		}
	}
	for s, want := range map[swarmConfigStore]string{staging: "s2", prod: "p1"} {
		got, err := s.load()
		if err != nil || string(got) != want {
			t.Errorf("%s read %q, %v; want %q", s.service, got, err, want)
		}
	}
	fe.mu.Lock()
	n := len(fe.configs)
	fe.mu.Unlock()
	if n != 2 {
		t.Errorf("%d configs left on the swarm, want one per agent", n)
	}
	if name := swarmConfigName(strings.Repeat("s", 80), 12); len(name) > swarmNameMax || !strings.HasSuffix(name, "-members-12") {
		t.Errorf("a long service name gave the config name %q", name)
	}
}
