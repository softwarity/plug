package main

import (
	"crypto/rand"
	"crypto/sha256"
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

// By DEFAULT every data volume of the workload is mounted, without being
// named: a takeover (-s) and --env-of ask the agent what the workload mounts
// (volumes-of) and put each volume under the session's temp directory, at its
// cluster path, then repoint the variables that name that path - the same
// "option B" the mounted secret files take, and for the same reason: the
// exact path is not always creatable here (macOS seals its root, Linux plug
// is not root). The process finds its data where its environment names it,
// and knows nothing. --no-mount turns that off, --no-mount=/a,/b leaves those
// out; --mount is the explicit form, at the exact path, for the process that
// hard-codes one.
//
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
	path   string // absolute path on this machine ("" for an automatic one the OS places itself)
	auto   bool   // mounted by default, not asked for: reported to the projection
}

func (m mountSpec) String() string { return m.name + ":" + m.volume + ":" + m.path }

// mountPolicy is what --no-mount said: off entirely, or these cluster paths
// left out of the automatic mounts. The zero value mounts everything.
type mountPolicy struct {
	off  bool
	drop map[string]bool
}

func parseNoMount(list string) mountPolicy {
	if list == "" {
		return mountPolicy{off: true}
	}
	p := mountPolicy{drop: map[string]bool{}}
	for _, v := range strings.Split(list, ",") {
		if v = strings.TrimSpace(v); v != "" {
			p.drop[filepath.ToSlash(v)] = true
		}
	}
	return p
}

// looksLikePathList tells "/a,/b" (a --no-mount value) from the command that
// follows the bare flag: absolute paths, commas, nothing else.
func looksLikePathList(s string) bool {
	if s == "" {
		return false
	}
	for _, v := range strings.Split(s, ",") {
		if !strings.HasPrefix(v, "/") {
			return false
		}
	}
	return true
}

// volumeMount is one automatic mount as the projection sees it: the cluster
// path, and where it is here. Under the session directory at the same path
// on macOS and Linux; a drive letter on Windows, where a directory cannot
// point at a share without a privilege.
type volumeMount struct {
	cluster, local string
}

// autoMounts is what the automatic mounts did for each workload, read by the
// environment projection to repoint the variables naming a volume path, and
// the session directory they sit under (option B).
var autoMounts = map[string]struct {
	dir    string
	mounts []volumeMount
}{}

// autoMountsFor is the projection's view, or nothing when the workload had
// no volume mounted.
func autoMountsFor(name string) []volumeMount {
	return autoMounts[name].mounts
}

// dockerMount is one mount made for a --dockerrun: nothing is mounted on
// this host - Docker Desktop cannot bind a network mount into its VM, it
// hangs on it - so the CONTAINER gets a docker volume of type cifs that the
// daemon's own kernel mounts, through the forward this process keeps open
// (host.docker.internal on Desktop, the host itself on Linux).
type dockerMount struct {
	spec   mountSpec
	target mountTarget
}

// dockerMounts is what startMounts collected for the --dockerrun in progress,
// read by dockerMountFlags.
var dockerMounts []dockerMount

// localizeVolumeEnv repoints the variables whose value names a mounted
// cluster path (or something under it) at the mount. The longest cluster
// path wins, so /data/logs mounted apart from /data goes to its own mount.
func localizeVolumeEnv(vars []string, mounts []volumeMount) []string {
	if len(mounts) == 0 {
		return vars
	}
	out := make([]string, len(vars))
	for i, kv := range vars {
		out[i] = kv
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(v, "/") {
			continue
		}
		best := volumeMount{}
		for _, m := range mounts {
			if (v == m.cluster || strings.HasPrefix(v, m.cluster+"/")) && len(m.cluster) > len(best.cluster) {
				best = m
			}
		}
		if best.cluster == "" {
			continue
		}
		rest := strings.TrimPrefix(v, best.cluster)
		out[i] = k + "=" + filepath.Join(best.local, filepath.FromSlash(rest))
	}
	return out
}

// winDrivePath catches a trailing Windows path (C:\data) before the colon
// split, since its drive letter carries the very separator the grammar uses.
var winDrivePath = regexp.MustCompile(`^(.*?):([A-Za-z]:(?:[\\/].*)?)$`)

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
	if isBareDrive(spec.path) {
		spec.path = strings.ToUpper(spec.path) + `\` // "Z:" is a drive; alone it would mean its current directory
	} else {
		spec.path = filepath.Clean(spec.path)
	}
	return spec, nil
}

func isAbsPath(p string) bool {
	return strings.HasPrefix(p, "/") || isBareDrive(p) || winDrivePath.MatchString("x:"+p)
}

// isBareDrive: "Z:" - a Windows drive letter, the local path a mount may take there.
func isBareDrive(p string) bool {
	return len(p) == 2 && p[1] == ':' && (p[0] >= 'A' && p[0] <= 'Z' || p[0] >= 'a' && p[0] <= 'z')
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

// mountReply is what mount-volume answers: where the helper is, by NAME.
type mountReply struct {
	host, port, share, user string
}

// mountTarget is everything the OS needs to mount one share: the helper by
// name (what Windows dials, through plug's own DNS), the local forward to it
// (what macOS and Linux dial), and the credential.
type mountTarget struct {
	mountReply
	pass  string
	local string // the forward's address, "" where the OS reaches the name itself
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
// retried, as the exposes' verify does. Every few seconds the agent is asked
// where the helper stands (mount-status) and the progress line says it - a
// task pending on a constrained node, a pod pulling its image - and when the
// budget runs out that last word is the error, not a bare timeout.
func mountHelperReady(tr sessionTransport, addr string, budget time.Duration, status func() string, p *progress) error {
	deadline := time.Now().Add(budget)
	lastAsk := time.Time{}
	last := ""
	for {
		c, err := tr.DialClusterTimeout(addr, 2*time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		if time.Since(lastAsk) > 3*time.Second {
			lastAsk = time.Now()
			if st := status(); st != "" {
				last = st
				p.step("helper " + st)
			}
		}
		if time.Now().After(deadline) {
			if last != "" {
				return fmt.Errorf("the mount helper did not answer within %s; its state: %s", budget, last)
			}
			return fmt.Errorf("the mount helper at %s did not answer within %s: %v", addr, budget, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// helperStatus asks the agent where the helper stands; "" when it cannot say
// (an agent too old for the verb answers "unknown command", which is no news).
func helperStatus(tr sessionTransport, m *liveMount, agentPort string) string {
	out, err := tr.Exec("mount-status " + m.spec.name + " " + m.spec.volume + " " + agentPort)
	if err != nil || !strings.HasPrefix(out, "status ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(out, "status "))
}

// mountForwardBind is where the local end of a mount's forward listens: the
// loopback, on a port the OS picks. A var so a test can make the listen fail
// (an unusable port) and watch what the failure leaves behind. Never
// reassigned outside tests.
var mountForwardBind = "127.0.0.1:0"

// mountSMB mounts one share at path with the OS's own SMB client and answers
// where it landed; unmountSMB undoes it. Both are vars over the per-OS bodies
// (mount_<os>.go), so the whole of startMounts can be driven without a share
// to mount: what is asked of the agent, what is recorded, what the teardown
// undoes. Never reassigned outside tests.
var (
	mountSMB   = mountSMBShare
	unmountSMB = unmountSMBShare
)

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
	explicit := cfg.mounts
	autoNames := autoMountNames(cfg)
	if len(explicit) == 0 && len(autoNames) == 0 {
		return func() {}, nil
	}
	if err := mountSupported(); err != nil && !cfg.dockerRun {
		if len(explicit) > 0 {
			return nil, err
		}
		// The automatic mounts are a default, not a demand: where the OS
		// cannot yet, the session runs without them and says so once.
		info("the workload's volumes are not mounted: %v", err)
		return func() {}, nil
	}
	dockerMounts = nil
	sweepOrphanMounts()
	tr, err := dialSession(cfg)
	if err != nil {
		return nil, err
	}
	// What the workloads mount, from the agent, each under the session dir at
	// its own cluster path. A workload with no volume mounts nothing and
	// costs one line; an agent too old for the verb, likewise.
	var specs []mountSpec
	if len(autoNames) > 0 {
		dir, derr := os.MkdirTemp("", "plug-vol-")
		if derr != nil {
			tr.Close()
			return nil, derr
		}
		chownToUser(dir)
		for _, name := range autoNames {
			paths, aerr := workloadVolumes(tr, name)
			if aerr != nil {
				info("%s: could not list the workload's volumes (%v); none mounted", name, aerr)
				continue
			}
			for _, p := range paths {
				if cfg.mountPolicy.drop[p] {
					continue
				}
				at := autoMountPath(dir, p)
				if cfg.dockerRun {
					at = p // the container's, at the cluster path
				}
				specs = append(specs, mountSpec{name: name, volume: p, path: at, auto: true})
			}
			if len(specs) > 0 {
				a := autoMounts[name]
				a.dir = dir
				autoMounts[name] = a
			}
		}
	}
	specs = append(specs, explicit...)
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
			if out, err := tr.Exec("unmount-volume " + m.spec.name + " " + m.spec.volume + " " + live.AgentPort()); err != nil || strings.HasPrefix(out, "error:") {
				info("could not release the mount helper for %s: %s%v", m.spec, out, err)
			}
			if m.unmark != nil {
				m.unmark()
			}
		}
		// The session directory the automatic mounts sat under: removed only
		// once nothing is mounted there any more - a RemoveAll through a live
		// mount would be the volume's files, not the directory.
		for name, a := range autoMounts {
			delete(autoMounts, name)
			busy := false
			for _, m := range a.mounts {
				if mountedAt(m.local) {
					busy = true
				}
			}
			if !busy && a.dir != "" {
				_ = os.RemoveAll(a.dir)
			}
		}
		unpinMountNames(cfg)
		tr.Close()
	}
	fail := func(err error) (func(), error) {
		stop()
		return nil, err
	}
	for _, spec := range specs {
		m := &liveMount{spec: spec, pass: mintMountPass()}
		if m.pass == "" {
			return fail(errors.New("no entropy to mint the mount credential"))
		}
		mounts = append(mounts, m)
		p := startProgress("mounting " + spec.volume + " of " + spec.name)
		p.step("asking the agent for a helper")
		reply, err := provisionMount(tr, m, live.AgentPort())
		if err != nil {
			p.done("failed")
			return fail(err)
		}
		pinMountName(cfg, reply.host)
		target := net.JoinHostPort(reply.host, reply.port)
		p.step("helper " + reply.host + " starting")
		if err := mountHelperReady(tr, target, 90*time.Second, func() string { return helperStatus(tr, m, live.AgentPort()) }, p); err != nil {
			p.done("failed")
			return fail(err)
		}
		p.step("mounting")
		t := mountTarget{mountReply: reply, pass: m.pass}
		if mountUsesForward() || cfg.dockerRun {
			fw, err := newMountForward(mountForwardBind, target, tr.DialCluster)
			if err != nil {
				// Ended like the other failures of this loop. Left open, the
				// progress line's spinner kept redrawing over the error.
				p.done("failed")
				return fail(err)
			}
			m.fw = fw
			t.local = fw.Addr()
		}
		if cfg.dockerRun {
			// Not mounted here: handed to the container as a cifs volume the
			// daemon mounts through the forward (dockerMountFlags).
			dockerMounts = append(dockerMounts, dockerMount{spec: spec, target: t})
			p.done("served for the container through " + t.local)
			continue
		}
		at, err := mountSMB(t, spec.path)
		if err != nil {
			p.done("failed")
			return fail(fmt.Errorf("mounting %s of %s at %s: %w", spec.volume, spec.name, spec.path, err))
		}
		m.spec.path = at
		m.mounted = true
		m.unmark = markMounted(m.spec, t.local, net.JoinHostPort(cfg.host, cfg.port))
		if spec.auto {
			a := autoMounts[spec.name]
			a.mounts = append(a.mounts, volumeMount{cluster: spec.volume, local: at})
			autoMounts[spec.name] = a
		}
		p.done("mounted at " + at + ", read-write, live")
	}
	if len(mounts) == 0 {
		tr.Close()
		return func() {}, nil
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
				pinMountName(cfg, reply.host)
				if m.fw != nil {
					m.fw.Retarget(net.JoinHostPort(reply.host, reply.port))
				}
			}
		}
	}()
	return stop, nil
}

// autoMountNames are the workloads whose volumes are mounted by default: the
// names -s takes over and the --env-of one, unless --no-mount said no.
func autoMountNames(cfg config) []string {
	if cfg.mountPolicy.off {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, e := range cfg.exposes {
		add(e.Name)
	}
	add(cfg.envPolicy.from)
	return out
}

// workloadVolumes asks the agent what the workload mounts as data. An agent
// that predates the verb answers "unknown command": nothing to mount, not
// an error - the session is as it was before the feature.
func workloadVolumes(tr sessionTransport, name string) ([]string, error) {
	out, err := tr.Exec("volumes-of " + name)
	if err != nil {
		return nil, err
	}
	return parseVolumesReply(out)
}

func parseVolumesReply(out string) ([]string, error) {
	out = strings.TrimSpace(out)
	if strings.Contains(out, "unknown command") {
		return nil, nil
	}
	if strings.HasPrefix(out, "error: ") {
		return nil, errors.New(strings.TrimPrefix(out, "error: "))
	}
	f := strings.Fields(out)
	if len(f) == 0 || f[0] != "volumes" {
		return nil, fmt.Errorf("unexpected answer from the agent: %q", out)
	}
	return f[1:], nil
}

// provisionMount asks the agent for the helper, under this session's current
// liveness port and the session's credential.
func provisionMount(tr sessionTransport, m *liveMount, agentPort string) (mountReply, error) {
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
	pid     int
	path    string
	spec    string
	local   string // the forward's address the OS client was pointed at
	cluster string // host:port of the agent the volume comes from
	auto    bool   // placed by plug (a takeover's data volumes), not asked for
	file    string
}

// markMounted records this process as holding path mounted, and returns the
// cleanup that forgets it. The file is named by the path, folded (a path is
// not a file name): one record per mountpoint.
func markMounted(spec mountSpec, local, cluster string) func() {
	file := filepath.Join(mountsDir(), recordName(spec.path))
	guardUserPath(file)
	if os.MkdirAll(mountsDir(), 0o700) != nil {
		return func() {}
	}
	auto := "no"
	if spec.auto {
		auto = "yes"
	}
	body := fmt.Sprintf("pid = %d\npath = %s\nspec = %s\nlocal = %s\ncluster = %s\nauto = %s\n",
		os.Getpid(), spec.path, spec, local, cluster, auto)
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
		// Cut short, the name is no longer the path's. The automatic mounts of
		// one workload all sit under the session directory, and on macOS that
		// prefix alone (/var/folders/<xx>/<id>/T/plug-vol-<n>/) is past the cut:
		// two volumes wrote ONE record, and the sweep after a crash saw one
		// mount. The head stays readable; the tail is a digest of the whole.
		d := sha256.Sum256([]byte(filepath.ToSlash(path)))
		sum = sum[:88] + hex.EncodeToString(d[:16])
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
			case "cluster":
				r.cluster = v
			case "auto":
				r.auto = v == "yes"
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
