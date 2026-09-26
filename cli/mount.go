package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// The live mount (--mount): a workload's volume - a Docker volume, a bind, a
// PVC - at a path on this machine, read AND written by the command, for the
// length of the session. Every mode: -s (the parked workload's volume), -c
// --env-of (another workload's), and the same wire format across the exec.
//
// Nothing is installed here. The agent starts a helper beside the workload
// that serves the volume over SMB (agent/mount.go); this side opens a local
// forward to it through the tunnel - what plug does for every service - and
// mounts it with the SMB client the OS ships with: mount_smbfs on macOS, the
// cifs module on Linux (mount(2), no cifs-utils), the redirector on Windows.
// The OS's client reconnects on its own, so a tunnel blip is a pause, not a
// broken mount; a reconnect re-provisions the helper (its label is immutable,
// like a signpost's) with the SAME credential, minted here, so the client's
// reconnection authenticates.
//
// What a crash leaves behind is recorded (~/.plug/mounts) the way served
// names are, so the next run and `plug doctor` can unmount it, and the agent's
// sweep reaps the helper whose session no longer answers.

// mountSpec is one --mount: the workload, what to mount of it, and where.
//
//	--mount /data                  the workload's /data, at /data here
//	--mount data:/srv/data         its volume `data`, at /srv/data here
//	--mount /data:/srv/data        its /data, at /srv/data here
//	--mount api:data:/srv/data     the volume `data` of `api` (explicit)
//
// The workload is implied when unnamed: the one -s name, or --env-of.
type mountSpec struct {
	name   string // workload name; "" until resolved against -s / --env-of
	volume string // volume/PVC name, or the absolute path it has in the workload
	path   string // absolute path on this machine
}

func (m mountSpec) String() string { return m.name + ":" + m.volume + ":" + m.path }

// winDrivePath catches a trailing Windows path (C:\data) before the colon
// split, since its drive letter carries the very separator the grammar uses.
var winDrivePath = regexp.MustCompile(`^(.*?):([A-Za-z]:[\\/].*)$`)

func parseMount(raw string) (mountSpec, error) {
	if raw == "" {
		return mountSpec{}, errors.New("--mount needs a value: [<name>:]<volume-or-path>[:<local-path>]")
	}
	var fields []string
	if m := winDrivePath.FindStringSubmatch(raw); m != nil {
		fields = append(strings.Split(m[1], ":"), m[2])
	} else {
		fields = strings.Split(raw, ":")
	}
	var spec mountSpec
	switch len(fields) {
	case 1:
		spec.volume, spec.path = fields[0], fields[0]
		if !isAbsPath(spec.volume) {
			return mountSpec{}, fmt.Errorf("--mount %s: a volume NAME needs the local path to mount it at (--mount %s:<local-path>); a PATH alone is mounted at the same path here", raw, raw)
		}
	case 2:
		spec.volume, spec.path = fields[0], fields[1]
	case 3:
		spec.name, spec.volume, spec.path = fields[0], fields[1], fields[2]
		if !dnsLabel(spec.name) {
			return mountSpec{}, fmt.Errorf("--mount %s: %q is not a valid service name", raw, spec.name)
		}
	default:
		return mountSpec{}, fmt.Errorf("--mount %s: too many fields ([<name>:]<volume-or-path>[:<local-path>])", raw)
	}
	if spec.volume == "" || strings.ContainsAny(spec.volume, " \t") || strings.Contains(spec.volume, "..") {
		return mountSpec{}, fmt.Errorf("--mount %s: %q is not a volume name or a path", raw, spec.volume)
	}
	if !isAbsPath(spec.path) {
		return mountSpec{}, fmt.Errorf("--mount %s: the local path must be absolute, got %q", raw, spec.path)
	}
	spec.path = filepath.Clean(spec.path)
	return spec, nil
}

func isAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || winDrivePath.MatchString("x:"+p)
}

var dnsLabelRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func dnsLabel(s string) bool { return dnsLabelRe.MatchString(s) }

// resolveMountNames fills in the workload every unnamed mount belongs to: the
// one name -s serves, or the --env-of one. Two -s names and no --env-of is an
// ambiguity to refuse, not to guess.
func resolveMountNames(specs []mountSpec, exposes []tunnel.ExposeSpec, envOf string) ([]mountSpec, error) {
	implied := envOf
	if implied == "" {
		seen := map[string]bool{}
		for _, e := range exposes {
			if !seen[e.Name] {
				seen[e.Name] = true
				if implied != "" {
					implied = "?"
				} else {
					implied = e.Name
				}
			}
		}
	}
	out := make([]mountSpec, 0, len(specs))
	for _, s := range specs {
		if s.name == "" {
			switch implied {
			case "":
				return nil, fmt.Errorf("--mount %s: which workload? name it (--mount <name>:%s:%s), or give -s / --env-of", s.volume, s.volume, s.path)
			case "?":
				return nil, fmt.Errorf("--mount %s: several names are served; name the workload (--mount <name>:%s:%s)", s.volume, s.volume, s.path)
			}
			s.name = implied
		}
		out = append(out, s)
	}
	return out, nil
}

// mintMountPass is the session's SMB secret: 20 random bytes, hex - one
// token, as the verb wants it, and never reused.
func mintMountPass() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// mountReply is what mount-volume answers: where the helper is.
type mountReply struct {
	host, port, share, user string
}

func parseMountReply(line string) (mountReply, error) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "error: ") {
		return mountReply{}, errors.New(strings.TrimPrefix(line, "error: "))
	}
	f := strings.Fields(line)
	if len(f) == 0 || f[0] != "mounted" {
		return mountReply{}, fmt.Errorf("unexpected answer from the agent: %q", line)
	}
	var r mountReply
	for _, kv := range f[1:] {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "host":
			r.host = v
		case "port":
			r.port = v
		case "share":
			r.share = v
		case "user":
			r.user = v
		}
	}
	if r.host == "" || r.port == "" || r.share == "" || r.user == "" {
		return mountReply{}, fmt.Errorf("incomplete answer from the agent: %q", line)
	}
	return r, nil
}

// mountForward is the local end the OS's SMB client dials: a listener on
// this machine, every connection carried through the tunnel to the helper.
// The target can change (a re-provisioned helper may get a new address), so
// it is read at every accept.
type mountForward struct {
	ln     net.Listener
	target atomic.Pointer[string]
	dial   func(addr string) (net.Conn, error)
	wg     sync.WaitGroup
}

func newMountForward(bind string, target string, dial func(string) (net.Conn, error)) (*mountForward, error) {
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %s for the mount: %w", bind, err)
	}
	f := &mountForward{ln: ln, dial: dial}
	f.target.Store(&target)
	f.wg.Add(1)
	go f.serve()
	return f, nil
}

func (f *mountForward) Addr() string { return f.ln.Addr().String() }

func (f *mountForward) Retarget(target string) { f.target.Store(&target) }

func (f *mountForward) serve() {
	defer f.wg.Done()
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			up, err := f.dial(*f.target.Load())
			if err != nil {
				return
			}
			defer up.Close()
			splice(c, up)
		}()
	}
}

func (f *mountForward) Close() {
	f.ln.Close()
	f.wg.Wait()
}

// splice copies both ways until either side ends; the SMB client and Samba
// both close cleanly, so a half-close is enough to let the other side drain.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// mountHelperReady waits for the helper to answer on its port: scheduling and
// smbd's start are seconds, a pod pulling its image can be more. Short dials,
// retried, as the exposes' verify does.
func mountHelperReady(tr *tunnel.Transport, addr string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for {
		c, err := tr.DialClusterTimeout(addr, 2*time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the mount helper at %s did not answer within %s: %v", addr, budget, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// liveMount is one mounted volume, everything its teardown needs.
type liveMount struct {
	spec    mountSpec
	pass    string
	fw      *mountForward
	unmark  func()
	mounted bool
}

// startMounts provisions every --mount before the command runs and returns
// the teardown that unmounts them all after it. Its own transport, like the
// exposes': the forwards and the liveness proof live on it for the session.
//
// The liveness proof is a remote forward this session holds - the same
// evidence a signpost carries - whose agent-side port the helper is labelled
// with. Nothing ever connects to it from this side (its local port refuses),
// the agent's sweep only asks whether it still answers.
func startMounts(cfg config) (func(), error) {
	if len(cfg.mounts) == 0 {
		return func() {}, nil
	}
	if err := mountSupported(); err != nil {
		return nil, err
	}
	sweepOrphanMounts()
	tr, err := dialTunnel(cfg)
	if err != nil {
		return nil, err
	}
	live, err := tr.Expose(tunnel.ExposeSpec{Name: "plug-mount", ClusterPort: "0", LocalPort: "1"})
	if err != nil {
		tr.Close()
		return nil, fmt.Errorf("opening the mount session's liveness forward: %w", err)
	}
	var mounts []*liveMount
	var done atomic.Bool
	stop := func() {
		done.Store(true)
		for i := len(mounts) - 1; i >= 0; i-- {
			m := mounts[i]
			if m.mounted {
				if err := unmountSMB(m.spec.path); err != nil {
					info("could not unmount %s: %v — plug doctor --fix will", m.spec.path, err)
				} else {
					info("unmounted %s", m.spec.path)
				}
			}
			if m.fw != nil {
				m.fw.Close()
			}
			if out, err := tr.Exec("unmount-volume " + m.spec.name + " " + m.spec.volume); err != nil || strings.HasPrefix(out, "error:") {
				info("could not release the mount helper for %s: %s%v", m.spec, out, err)
			}
			if m.unmark != nil {
				m.unmark()
			}
		}
		tr.Close()
	}
	fail := func(err error) (func(), error) {
		stop()
		return nil, err
	}
	for _, spec := range cfg.mounts {
		m := &liveMount{spec: spec, pass: mintMountPass()}
		if m.pass == "" {
			return fail(errors.New("no entropy to mint the mount credential"))
		}
		mounts = append(mounts, m)
		reply, err := provisionMount(tr, m, live.AgentPort())
		if err != nil {
			return fail(err)
		}
		target := net.JoinHostPort(reply.host, reply.port)
		info("mount helper for %s of %s is up at %s", spec.volume, spec.name, target)
		if err := mountHelperReady(tr, target, 90*time.Second); err != nil {
			return fail(err)
		}
		fw, err := newMountForward(mountBindAddr(), target, tr.DialCluster)
		if err != nil {
			return fail(err)
		}
		m.fw = fw
		if err := mountSMB(fw.Addr(), reply.share, reply.user, m.pass, spec.path); err != nil {
			return fail(fmt.Errorf("mounting %s of %s at %s: %w", spec.volume, spec.name, spec.path, err))
		}
		m.mounted = true
		m.unmark = markMounted(spec, fw.Addr())
		info("mounted %s of %s at %s (read-write, live)", spec.volume, spec.name, spec.path)
	}
	// A reconnect re-allocates the liveness port: re-provision every helper
	// under the new one, or the sweep reaps them within the minute. The hook
	// must not block (see Exposed.OnRearm); the work happens on its own.
	kick := make(chan struct{}, 1)
	live.OnRearm(func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	})
	go func() {
		for range kick {
			if done.Load() {
				return
			}
			for _, m := range mounts {
				reply, err := provisionMount(tr, m, live.AgentPort())
				if err != nil {
					info("re-provisioning the mount helper for %s after a reconnect: %v", m.spec, err)
					continue
				}
				m.fw.Retarget(net.JoinHostPort(reply.host, reply.port))
			}
		}
	}()
	return stop, nil
}

// provisionMount asks the agent for the helper, under this session's current
// liveness port and the session's credential.
func provisionMount(tr *tunnel.Transport, m *liveMount, agentPort string) (mountReply, error) {
	out, err := tr.Exec("mount-volume " + m.spec.name + " " + m.spec.volume + " " + agentPort + " " + m.pass)
	if err != nil {
		return mountReply{}, fmt.Errorf("asking the agent to mount %s of %s: %w", m.spec.volume, m.spec.name, err)
	}
	if strings.Contains(out, "unknown command") {
		return mountReply{}, errors.New("the cluster agent predates --mount (needs plug ≥ 2.20). Upgrade the agent (redeploy the softwarity/plug image), then run again")
	}
	return parseMountReply(out)
}

// ── Records: what a crash leaves behind ──────────────────────────────────────

func mountsDir() string { return filepath.Join(plugDir(), "mounts") }

// mountRecord is what a session leaves about a mount, so the next run and
// doctor can tell a live mount from a dead one and unmount the latter.
type mountRecord struct {
	pid   int
	path  string
	spec  string
	local string // the forward's address the OS client was pointed at
	file  string
}

// markMounted records this process as holding path mounted, and returns the
// cleanup that forgets it. The file is named by the path, folded (a path is
// not a file name): one record per mountpoint.
func markMounted(spec mountSpec, local string) func() {
	file := filepath.Join(mountsDir(), recordName(spec.path))
	guardUserPath(file)
	if os.MkdirAll(mountsDir(), 0o700) != nil {
		return func() {}
	}
	body := fmt.Sprintf("pid = %d\npath = %s\nspec = %s\nlocal = %s\n", os.Getpid(), spec.path, spec, local)
	if os.WriteFile(file, []byte(body), 0o600) != nil {
		return func() {}
	}
	chownToUser(mountsDir())
	chownToUser(file)
	return func() { _ = os.Remove(file) }
}

func recordName(path string) string {
	sum := hex.EncodeToString([]byte(filepath.ToSlash(path)))
	if len(sum) > 120 {
		sum = sum[:120]
	}
	return sum
}

// mountRecords reads every record left under ~/.plug/mounts.
func mountRecords() []mountRecord {
	entries, err := os.ReadDir(mountsDir())
	if err != nil {
		return nil
	}
	var out []mountRecord
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		file := filepath.Join(mountsDir(), e.Name())
		b, err := readUserOwnedFile(file)
		if err != nil {
			continue
		}
		r := mountRecord{file: file}
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, " = ")
			if !ok {
				continue
			}
			switch k {
			case "pid":
				r.pid, _ = strconv.Atoi(v)
			case "path":
				r.path = v
			case "spec":
				r.spec = v
			case "local":
				r.local = v
			}
		}
		if r.path != "" {
			out = append(out, r)
		}
	}
	return out
}

// orphanMounts are the records whose session is gone: a dead pid. Whether the
// path is still mounted is the next question, and the OS's to answer.
func orphanMounts(records []mountRecord, alive func(int) bool) []mountRecord {
	var out []mountRecord
	for _, r := range records {
		if r.pid == 0 || !alive(r.pid) {
			out = append(out, r)
		}
	}
	return out
}

// doctorMounts is the doctor's view of the same records: a mount whose
// session is gone and whose path is still mounted is the one thing a crash
// can leave on this machine that no next run cleans by itself unless it
// mounts again; --fix unmounts it. A record with nothing mounted is just
// forgotten. The verdict is pure, so it is tested without a mount.
func doctorMounts(add func(check)) {
	orphans := orphanMounts(mountRecords(), processAlive)
	var stale []mountRecord
	for _, r := range orphans {
		if mountedAt(r.path) {
			stale = append(stale, r)
		} else {
			_ = os.Remove(r.file)
		}
	}
	if len(stale) == 0 {
		return
	}
	if doctorFix {
		var failed []string
		for _, r := range stale {
			if err := unmountSMB(r.path); err != nil {
				failed = append(failed, r.path+" ("+err.Error()+")")
				continue
			}
			_ = os.Remove(r.file)
		}
		add(mountsVerdict(len(stale)-len(failed), failed))
		return
	}
	var paths []string
	for _, r := range stale {
		paths = append(paths, r.path)
	}
	add(mountsVerdict(0, paths))
}

// mountsVerdict is the check doctorMounts reports: how many stale mounts were
// unmounted, and which remain (or, without --fix, which there are).
func mountsVerdict(fixed int, remaining []string) check {
	c := check{area: "local", name: "live mounts"}
	switch {
	case len(remaining) == 0:
		c.status, c.detail = stOK, fmt.Sprintf("%d mount(s) left by a dead session unmounted", fixed)
	case fixed == 0 && !doctorFix:
		c.status = stFail
		c.detail = fmt.Sprintf("%d mount(s) left by a session that died: %s", len(remaining), strings.Join(remaining, ", "))
		c.remedy = "plug doctor --fix   (unmounts them; or by hand: umount <path>)"
	default:
		c.status = stFail
		c.detail = fmt.Sprintf("%d unmounted; still mounted: %s", fixed, strings.Join(remaining, ", "))
		c.remedy = "unmount by hand (umount <path>; on macOS diskutil unmount <path>), then plug doctor again"
	}
	return c
}

// sweepOrphanMounts is what a run does before mounting anything: a previous
// session that died with a mount up left it there, and mounting over it would
// fail on a busy path. Unmount what is dead, forget it, say so.
func sweepOrphanMounts() {
	for _, r := range orphanMounts(mountRecords(), processAlive) {
		if mountedAt(r.path) {
			if err := unmountSMB(r.path); err != nil {
				info("a previous session left %s mounted and it cannot be unmounted: %v (plug doctor --fix)", r.path, err)
				continue
			}
			info("unmounted %s, left by a previous session", r.path)
		}
		_ = os.Remove(r.file)
	}
}
