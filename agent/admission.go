package agent

// Admission by approval: the standalone agent that lets in its members only.
//
// Standalone plug authenticates the SOFTWARE: the tunnel key is in every
// published binary, so reaching the port is the whole authorization. That is
// the open mode, still the default. PLUG_ADMISSION=invite at deploy time
// turns this file on, and the agent then keeps a ROSTER:
//
//   - while it has no member, the first key that proves itself becomes one;
//   - after that an unknown key is recorded as a request, and refused;
//   - a member accepts a request (`plug accept <key>`, `plug requests`) or
//     revokes a member (`plug members`), and nobody else can.
//
// Nothing is typed by the newcomer beyond the install line they already use:
// plug makes the machine's key, presents it, and prints what to ask of whom.
// There is no code to pass and nothing that expires but a stale request.
//
// The roster is a few kilobytes of JSON kept where the orchestrator keeps
// state (rosterStore below), and every refusal to read it is CLOSED: an invite
// agent that cannot read its roster does not start, rather than falling back
// to letting anyone in.
//
// It is a Host like standaloneHost, plus the admitter methods the SSH server
// looks for (sshserver.go). An embedder's Host does not implement them, so a
// gateway sees no change: its own Host still decides who gets in.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// admissionEnv chooses the mode at deploy time: unset or "open" is the
	// standalone agent as it always was, "invite" this file.
	admissionEnv = "PLUG_ADMISSION"

	// admissionModeEnv tells a verb subprocess which kind of agent it answers
	// for when admission verbs reach it: an open standalone agent, or a gateway
	// whose own Host manages the members. An invite agent answers those verbs
	// in the server process and never gets here.
	admissionModeEnv = "PLUG_ADMISSION_MODE"

	// requestLife is how long a request waits without a new attempt before it
	// is forgotten. Each attempt from the same machine starts it again.
	requestLife = 7 * 24 * time.Hour

	// maxRequests bounds the waiting list. Anyone who reaches the port can add
	// to it; past this the oldest attempt is dropped, so the list stays one a
	// member can read.
	maxRequests = 20

	// rosterFresh is how long a read of the store answers Verify. Every write
	// happens in this process, so this only bounds how soon a roster changed
	// from outside (a reset) is seen.
	rosterFresh = 5 * time.Second

	// seenEvery bounds how often a member's last connection is written: once
	// an hour is a useful "seen" and keeps the store from being rewritten on
	// every command.
	seenEvery = time.Hour

	// shortFingerprint is how much of the SHA256 fingerprint names a key to a
	// human: 12 base64 characters, 72 bits. Nobody makes a second key that
	// starts the same; with 8 a patient machine could.
	shortFingerprint = 12

	// admissionChallenge names the keyboard-interactive exchange the server
	// runs with a key it does not know (sshserver.go). The client recognises
	// it by this name and answers it; anything else is a human's prompt.
	admissionChallenge = "plug-admission"
)

// What the server says in that exchange, in the instruction field. The words
// a person reads are the CLI's: it knows the profile, which the agent does not.
const (
	sayAskName = "ask-name" // one question: who is this machine
	sayWelcome = "welcome"  // first member, admitted on the spot
	sayOwnKey  = "own-key"  // the shared key was offered: plug needs one of its own
	sayPending = "pending"  // recorded, waiting: "pending fp=<short> members=a,b"
	sayDown    = "unavailable"
)

type rosterMember struct {
	Name        string    `json:"name"`
	Key         string    `json:"key"` // authorized_keys form
	Fingerprint string    `json:"fingerprint"`
	By          string    `json:"by,omitempty"` // empty for the first member
	Since       time.Time `json:"since"`
	Seen        time.Time `json:"seen,omitempty"`
}

type rosterRequest struct {
	Name        string    `json:"name"`
	Key         string    `json:"key"`
	Fingerprint string    `json:"fingerprint"`
	From        string    `json:"from,omitempty"`
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
}

type roster struct {
	Members  []rosterMember  `json:"members"`
	Requests []rosterRequest `json:"requests"`
}

func (r *roster) member(fp string) *rosterMember {
	for i := range r.Members {
		if r.Members[i].Fingerprint == fp {
			return &r.Members[i]
		}
	}
	return nil
}

func (r *roster) memberNames() []string {
	out := make([]string, 0, len(r.Members))
	for _, m := range r.Members {
		out = append(out, m.Name)
	}
	return out
}

// prune forgets the requests nobody repeated for requestLife, then keeps the
// maxRequests most recent attempts.
func (r *roster) prune(now time.Time) {
	kept := r.Requests[:0]
	for _, q := range r.Requests {
		if now.Sub(q.Last) < requestLife {
			kept = append(kept, q)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Last.After(kept[j].Last) })
	if len(kept) > maxRequests {
		kept = kept[:maxRequests]
	}
	r.Requests = kept
}

// rosterStore is where the roster outlives the agent. load answers nil with no
// error for a store that holds nothing yet; any error is a refusal to read,
// which an invite agent treats as closed.
type rosterStore interface {
	load() ([]byte, error)
	save([]byte) error
	where() string
}

// inviteHost is the Host of an agent that admits by approval.
type inviteHost struct {
	// The keys built into plug. They authenticate the software, so in this
	// mode they are never a member and never a request: whoever presents one
	// is told to let plug make a key of its own.
	shared  []ssh.PublicKey
	keyPath string
	store   rosterStore
	logf    func(string, ...any)
	now     func() time.Time

	mu     sync.Mutex
	r      roster
	readAt time.Time
}

// Invite returns the Host of an agent that admits its members by approval, its
// roster kept in the store the orchestrator offers. An error means the agent
// must not start: it could not read who its members are.
func Invite(authorizedKeysFile, stateDirectory string) (Host, error) {
	if authorizedKeysFile == "" {
		authorizedKeysFile = authorizedKeysPath
	}
	if stateDirectory == "" {
		stateDirectory = stateDir
	}
	b, err := os.ReadFile(authorizedKeysFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", authorizedKeysFile, err)
	}
	shared, err := parseAuthorizedKeys(b)
	if err != nil {
		return nil, err
	}
	store, err := rosterStoreHere()
	if err != nil {
		return nil, err
	}
	return newInviteHost(shared, stateDirectory+"/host_key", store, nil)
}

func newInviteHost(shared []ssh.PublicKey, keyPath string, store rosterStore, logf func(string, ...any)) (*inviteHost, error) {
	if logf == nil {
		logf = func(format string, a ...any) { fmt.Fprintf(os.Stderr, "plug-agent: "+format+"\n", a...) }
	}
	h := &inviteHost{shared: shared, keyPath: keyPath, store: store, logf: logf, now: time.Now}
	if err := h.read(); err != nil {
		return nil, fmt.Errorf("this agent admits by approval (%s=invite) and cannot read its members from %s: %w",
			admissionEnv, store.where(), err)
	}
	if len(h.r.Members) == 0 {
		logf("admission by approval, no member yet: the first key to connect becomes one")
	} else {
		logf("admission by approval, %d member(s), %d request(s) waiting, kept in %s",
			len(h.r.Members), len(h.r.Requests), store.where())
	}
	return h, nil
}

// read loads the roster from the store. Called with mu held, or before the
// host is shared.
func (h *inviteHost) read() error {
	b, err := h.store.load()
	if err != nil {
		return err
	}
	var r roster
	if len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("the roster does not parse: %w", err)
		}
	}
	h.r = r
	h.readAt = h.now()
	return nil
}

// fresh re-reads a roster older than rosterFresh. A store that fails now,
// after the agent started on it, leaves the last roster in force: it was
// valid, and a store's bad minute must not lock every member out.
func (h *inviteHost) fresh() {
	if h.now().Sub(h.readAt) < rosterFresh {
		return
	}
	if err := h.read(); err != nil {
		h.logf("cannot re-read the members from %s, keeping the last known list: %v", h.store.where(), err)
		h.readAt = h.now()
	}
}

func (h *inviteHost) write() error {
	h.r.prune(h.now())
	b, err := json.MarshalIndent(h.r, "", "  ")
	if err != nil {
		return err
	}
	if err := h.store.save(b); err != nil {
		h.logf("cannot write the members to %s: %v", h.store.where(), err)
		return err
	}
	return nil
}

func (h *inviteHost) HostKey() (ssh.Signer, error) { return hostKeySigner(h.keyPath) }

// Verify lets a member in, under its name. Free of side effects but the
// "seen" stamp: it can be asked about a key the client has not signed yet, so
// it must never record anything that matters (see admit).
func (h *inviteHost) Verify(key ssh.PublicKey) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fresh()
	m := h.r.member(ssh.FingerprintSHA256(key))
	if m == nil {
		return "", false
	}
	if now := h.now(); now.Sub(m.Seen) >= seenEvery {
		m.Seen = now
		_ = h.write() // best effort: a stamp, not a decision
	}
	return m.Name, true
}

func (h *inviteHost) Served(NameEvent) {}
func (h *inviteHost) Unserved(string)  {}

func (h *inviteHost) isShared(key ssh.PublicKey) bool {
	for _, s := range h.shared {
		if s.Type() == key.Type() && string(s.Marshal()) == string(key.Marshal()) {
			return true
		}
	}
	return false
}

// admitted is the outcome of a key Verify refused: let in (first member, or a
// member accepted a moment ago), or not, with what to tell the client.
type admitted struct {
	ok  bool
	who string
	say string
}

// admit decides for a key Verify refused, ONCE THE CLIENT HAS PROVEN IT HOLDS
// IT: the server calls this after the signature checked (sshserver.go), never
// for a key that was only offered. That order is the whole guard against
// putting somebody else's public key on the waiting list.
func (h *inviteHost) admit(key ssh.PublicKey, name, from string) admitted {
	if h.isShared(key) {
		return admitted{say: sayOwnKey}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fresh()
	fp := ssh.FingerprintSHA256(key)
	if m := h.r.member(fp); m != nil {
		return admitted{ok: true, who: m.Name}
	}
	now := h.now()
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if len(h.r.Members) == 0 {
		h.r.Members = append(h.r.Members, rosterMember{Name: name, Key: line, Fingerprint: fp, Since: now, Seen: now})
		h.dropRequest(fp)
		if err := h.write(); err != nil {
			h.r.Members = nil
			return admitted{say: sayDown}
		}
		h.logf("first member: %s %s from %s", name, fp, from)
		return admitted{ok: true, who: name, say: sayWelcome}
	}
	found := false
	for i := range h.r.Requests {
		if q := &h.r.Requests[i]; q.Fingerprint == fp {
			q.Name, q.From, q.Last = name, from, now
			found = true
		}
	}
	if !found {
		h.r.Requests = append(h.r.Requests, rosterRequest{Name: name, Key: line, Fingerprint: fp, From: from, First: now, Last: now})
		h.logf("request from %s %s at %s, waiting for a member", name, fp, from)
	}
	_ = h.write() // the request is a courtesy; the refusal stands either way
	return admitted{say: fmt.Sprintf("%s fp=%s members=%s", sayPending, shortFP(fp), strings.Join(h.r.memberNames(), ","))}
}

func (h *inviteHost) dropRequest(fp string) {
	kept := h.r.Requests[:0]
	for _, q := range h.r.Requests {
		if q.Fingerprint != fp {
			kept = append(kept, q)
		}
	}
	h.r.Requests = kept
}

// The admission verbs, answered in the server process: the roster lives here,
// and a write from another process would race the one this one is making.
// Reached only by a member, since only a member has a tunnel session.
var admissionVerbs = map[string]bool{"members": true, "requests": true, "accept": true, "revoke": true}

// verb answers one admission verb for the member `caller`. revoked is the
// fingerprint whose sessions must end, when a member was revoked.
func (h *inviteHost) verb(caller string, cmd []string) (reply, revoked string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fresh()
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format(time.RFC3339)
	}
	switch cmd[0] {
	case "members":
		var b strings.Builder
		for _, m := range h.r.Members {
			fmt.Fprintf(&b, "member\t%s\t%s\t%s\t%s\t%s\n", m.Fingerprint, m.Name, firstNonEmpty(m.By, "-"), stamp(m.Since), stamp(m.Seen))
		}
		return strings.TrimSuffix(b.String(), "\n"), ""
	case "requests":
		h.r.prune(h.now())
		var b strings.Builder
		for _, q := range h.r.Requests {
			fmt.Fprintf(&b, "request\t%s\t%s\t%s\t%s\n", q.Fingerprint, q.Name, firstNonEmpty(q.From, "-"), stamp(q.Last))
		}
		return strings.TrimSuffix(b.String(), "\n"), ""
	case "accept":
		if len(cmd) != 2 {
			return "error: usage: accept <key>", ""
		}
		i, err := pick(cmd[1], len(h.r.Requests), func(i int) string { return h.r.Requests[i].Fingerprint })
		if err != nil {
			return "error: " + err.Error() + h.waitingList(), ""
		}
		q := h.r.Requests[i]
		h.r.Members = append(h.r.Members, rosterMember{Name: q.Name, Key: q.Key, Fingerprint: q.Fingerprint, By: caller, Since: h.now()})
		h.dropRequest(q.Fingerprint)
		if err := h.write(); err != nil {
			_ = h.read()
			return "error: the roster could not be written, nothing changed: " + err.Error(), ""
		}
		h.logf("%s accepted %s %s", firstNonEmpty(caller, "a member"), q.Name, q.Fingerprint)
		return "ok accepted " + q.Name + " " + q.Fingerprint, ""
	case "revoke":
		if len(cmd) != 2 {
			return "error: usage: revoke <key>", ""
		}
		i, err := pick(cmd[1], len(h.r.Members), func(i int) string { return h.r.Members[i].Fingerprint })
		if err != nil {
			return "error: " + err.Error(), ""
		}
		m := h.r.Members[i]
		if len(h.r.Members) == 1 {
			return fmt.Sprintf("error: %s is the last member: without one, the next key to connect would become the first", m.Name), ""
		}
		h.r.Members = append(h.r.Members[:i:i], h.r.Members[i+1:]...)
		if err := h.write(); err != nil {
			_ = h.read()
			return "error: the roster could not be written, nothing changed: " + err.Error(), ""
		}
		h.logf("%s revoked %s %s", firstNonEmpty(caller, "a member"), m.Name, m.Fingerprint)
		return "ok revoked " + m.Name + " " + m.Fingerprint, m.Fingerprint
	}
	return fmt.Sprintf("error: unknown command %q", cmd[0]), ""
}

func (h *inviteHost) waitingList() string {
	if len(h.r.Requests) == 0 {
		return "; no request is waiting"
	}
	names := make([]string, 0, len(h.r.Requests))
	for _, q := range h.r.Requests {
		names = append(names, q.Name+" "+shortFP(q.Fingerprint))
	}
	return "; waiting: " + strings.Join(names, ", ")
}

// pick finds the one entry a key names: its whole fingerprint, with or without
// "SHA256:", or the start of it, shortFingerprint characters at least. Case
// matters, since it is base64.
func pick(arg string, n int, fp func(int) string) (int, error) {
	want := strings.TrimPrefix(arg, "SHA256:")
	if len(want) < shortFingerprint {
		return 0, fmt.Errorf("%q is too short to name a key: give the %d characters plug printed, or more", arg, shortFingerprint)
	}
	found := -1
	for i := 0; i < n; i++ {
		if strings.HasPrefix(strings.TrimPrefix(fp(i), "SHA256:"), want) {
			if found >= 0 {
				return 0, fmt.Errorf("%q names more than one key: give more of it", arg)
			}
			found = i
		}
	}
	if found < 0 {
		return 0, fmt.Errorf("no key %s", arg)
	}
	return found, nil
}

// shortFP is the part of a fingerprint a person copies: the first
// shortFingerprint characters after "SHA256:".
func shortFP(fp string) string {
	s := strings.TrimPrefix(fp, "SHA256:")
	if len(s) > shortFingerprint {
		s = s[:shortFingerprint]
	}
	return s
}

// cleanName is the machine's name as the client gave it, reduced to what can
// sit in a list and in a log line: lower case, letters, digits and . _ @ -,
// 64 at most. It names, it never authorizes: a request is accepted by its key.
func cleanName(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || strings.ContainsRune("._@-", c) {
			b.WriteRune(c)
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}

// admissionMode is what a verb subprocess is told about the agent it answers
// for, when admission verbs reach it (see admissionModeEnv).
func admissionMode(h Host) string {
	switch h.(type) {
	case *inviteHost:
		return "invite"
	case *standaloneHost:
		return "open"
	}
	return "gateway"
}

// doAdmission answers the admission verbs where there is no roster: an open
// agent, or a gateway's.
func doAdmission(cmd []string) {
	if os.Getenv(admissionModeEnv) == "gateway" {
		answer("error: the members of this cluster are managed by the gateway that serves it")
	}
	answer("error: this cluster admits anyone who reaches its port; deploy it with %s=invite to admit by approval", admissionEnv)
}

// ---- where the roster lives ----

// membersDir is where a Docker agent keeps its roster: a named volume mounted
// there, since Docker has no other storage that survives the container.
const membersDir = "/var/lib/plug/members"

// k8sRosterSecret names the Secret a Kubernetes agent keeps its roster in.
// The manifest creates it empty, and the Role grants get and patch on this
// one name and no other.
const k8sRosterSecret = "plug-members"

// rosterStoreHere picks the store the orchestrator offers: a Secret on
// Kubernetes, a config on Swarm (the agent can move between managers, a volume
// cannot), a volume on Docker.
func rosterStoreHere() (rosterStore, error) {
	if k8sAvailable() {
		return &k8sSecretStore{ns: k8sNamespace(), name: k8sRosterSecret}, nil
	}
	if dockerAvailable() {
		if self, err := dockerSelf(); err == nil && self.service != "" {
			return &swarmConfigStore{service: self.service}, nil
		}
	}
	if st, err := os.Stat(membersDir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("this agent admits by approval (%s=invite) and has nowhere to keep its members: "+
			"mount a volume at %s (compose: `- plug-members:%s`)", admissionEnv, membersDir, membersDir)
	}
	return &fileStore{path: filepath.Join(membersDir, "roster.json")}, nil
}

type fileStore struct{ path string }

func (f *fileStore) where() string { return f.path }

func (f *fileStore) load() ([]byte, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// save writes beside and renames, so a crash mid-write leaves the previous
// roster rather than half of one.
func (f *fileStore) save(b []byte) error {
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// k8sSecretStore keeps the roster in one key of a Secret the manifest created.
// Written by merge patch, so the Secret's labels and kubectl's own annotation
// stay as the manifest left them, and a later `kubectl apply` of a manifest
// that carries no data leaves the roster alone.
type k8sSecretStore struct{ ns, name string }

const k8sRosterKey = "roster.json"

func (k *k8sSecretStore) where() string { return "the Secret " + k.ns + "/" + k.name }

func (k *k8sSecretStore) path() string {
	return "/api/v1/namespaces/" + k.ns + "/secrets/" + k.name
}

func (k *k8sSecretStore) load() ([]byte, error) {
	var s struct {
		Data map[string]string `json:"data"`
	}
	code, err := k8sAPI("GET", k.path(), nil, &s)
	switch {
	case code == 404:
		return nil, fmt.Errorf("it does not exist: the manifest creates it empty, apply it again")
	case code == 403:
		return nil, fmt.Errorf("the agent may not read it: its Role needs get and patch on secrets named %s", k.name)
	case err != nil:
		return nil, err
	}
	v, ok := s.Data[k8sRosterKey]
	if !ok {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(v)
}

func (k *k8sSecretStore) save(b []byte) error {
	code, err := k8sMergePatch(k.path(), map[string]any{
		"data": map[string]string{k8sRosterKey: base64.StdEncoding.EncodeToString(b)},
	})
	if code == 403 {
		return fmt.Errorf("the agent may not write it: its Role needs patch on secrets named %s", k.name)
	}
	return err
}

// swarmConfigStore keeps the roster in a Swarm config. A config cannot be
// changed, so each write creates the next one and removes the older: the
// highest sequence number is the roster, and a write that died between the two
// steps leaves an extra config the next write removes.
//
// The configs belong to the agent's SERVICE, by label: a swarm is shared, and
// two agents on it (one stack each, a staging and a production) admitting the
// same members because they read the same configs would be one roster for two
// clusters that were meant to be separate.
type swarmConfigStore struct{ service string }

const (
	swarmRosterLabel = "plug.members"     // the agent's service
	swarmSeqLabel    = "plug.members.seq" // which write this is
	swarmNameMax     = 64                 // what the Docker API accepts for a config name
)

func (s swarmConfigStore) where() string {
	return "the Swarm configs labelled " + swarmRosterLabel + "=" + s.service
}

type swarmConfig struct {
	ID   string `json:"ID"`
	Spec struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
		Data   []byte            `json:"Data"`
	} `json:"Spec"`
}

func (s swarmConfigStore) list() ([]swarmConfig, error) {
	filters := url.QueryEscape(`{"label":["` + swarmRosterLabel + `=` + s.service + `"]}`)
	var out []swarmConfig
	if _, err := dockerAPI("GET", "/configs?filters="+filters, nil, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return swarmSeq(out[i]) < swarmSeq(out[j]) })
	return out, nil
}

func swarmSeq(c swarmConfig) int {
	n, err := strconv.Atoi(c.Spec.Labels[swarmSeqLabel])
	if err != nil {
		return -1
	}
	return n
}

// swarmConfigName is what `docker config ls` shows for write n of service's
// roster: the service first, so the list says whose it is, cut to what the API
// accepts. The label is what the agent reads, never the name.
func swarmConfigName(service string, n int) string {
	suffix := "-members-" + strconv.Itoa(n)
	if len(service)+len(suffix) > swarmNameMax {
		service = service[:swarmNameMax-len(suffix)]
	}
	return service + suffix
}

func (s swarmConfigStore) load() ([]byte, error) {
	cs, err := s.list()
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, nil
	}
	return cs[len(cs)-1].Spec.Data, nil
}

func (s swarmConfigStore) save(b []byte) error {
	cs, err := s.list()
	if err != nil {
		return err
	}
	next := 1
	if len(cs) > 0 {
		next = swarmSeq(cs[len(cs)-1]) + 1
	}
	body := map[string]any{
		"Name":   swarmConfigName(s.service, next),
		"Labels": map[string]string{swarmRosterLabel: s.service, swarmSeqLabel: strconv.Itoa(next)},
		"Data":   b, // []byte marshals as base64, which is what the API wants
	}
	if _, err := dockerAPI("POST", "/configs/create", body, nil); err != nil {
		return err
	}
	for _, c := range cs {
		_, _ = dockerAPI("DELETE", "/configs/"+c.ID, nil, nil)
	}
	return nil
}
