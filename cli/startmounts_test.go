package main

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// startMounts is the client side of the live mount: what it asks the agent,
// what it records on this machine, what the projection sees and what the
// teardown undoes. Driven here over the in-memory transport, with the OS's
// SMB client stood in by a fake, since nothing in these is about SMB.

// mountAgent answers like an agent whose workload geo mounts two volumes.
func mountAgent(cmd string) (string, error) {
	switch {
	case cmd == "volumes-of geo":
		return "volumes /data /logs", nil
	case strings.HasPrefix(cmd, "mount-volume "):
		f := strings.Fields(cmd)
		return "mounted host=plug-mnt-" + strings.Trim(f[2], "/") + " port=445 share=vol user=plug", nil
	case strings.HasPrefix(cmd, "mount-status "):
		return "status running", nil
	case strings.HasPrefix(cmd, "unmount-volume "):
		return "ok", nil
	}
	return "error: unknown command", nil
}

// fakeSMB stands in for the OS's SMB client: it records every mount and
// unmount and answers a path of its own on Windows, where the real one picks
// a drive letter.
type fakeSMB struct {
	mounted   []mountTarget
	at        []string
	unmounted []string
	err       error
}

func installFakeSMB(t *testing.T) *fakeSMB {
	t.Helper()
	savedMount, savedUnmount := mountSMB, unmountSMB
	t.Cleanup(func() { mountSMB, unmountSMB = savedMount, savedUnmount })
	s := &fakeSMB{}
	mountSMB = func(target mountTarget, path string) (string, error) {
		if s.err != nil {
			return "", s.err
		}
		if path == "" {
			path = string(rune('Z'-len(s.at))) + `:\`
		}
		s.mounted = append(s.mounted, target)
		s.at = append(s.at, path)
		return path, nil
	}
	unmountSMB = func(path string) error { s.unmounted = append(s.unmounted, path); return nil }
	return s
}

// fakeMountSession is a fakeSession whose cluster dials answer at once, the way
// a helper that is up does.
func fakeMountSession(reply func(string) (string, error)) *fakeSession {
	f := newFakeSession(reply)
	f.dial = func(string) (net.Conn, error) {
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	return f
}

var hexPass = regexp.MustCompile(`^[0-9a-f]{40}$`)

// A takeover mounts every data volume of the workload without being asked,
// each under the session directory at its cluster path, labelled with the
// session's liveness port and a credential minted here; each is recorded as
// automatic, and the projection can repoint the variables naming it.
func TestStartMountsMountsTheWorkloadsVolumesByDefault(t *testing.T) {
	sandboxHome(t)
	tmp := sandboxTemp(t)
	smb := installFakeSMB(t)
	f := fakeMountSession(mountAgent)
	installFakeSession(t, f)
	cfg := config{host: "agent.example", port: "2222", exposes: exposeSpecs("geo:80:3000")}

	stop, err := startMounts(cfg)
	if err != nil {
		t.Fatalf("startMounts: %v", err)
	}
	asked := f.askedPrefixed("mount-volume ")
	if len(asked) != 2 {
		t.Fatalf("mount-volume asked %d time(s), want one per volume: %v", len(asked), f.verbs())
	}
	for i, want := range []string{"mount-volume geo /data 40001 ", "mount-volume geo /logs 40001 "} {
		if !strings.HasPrefix(asked[i], want) || !hexPass.MatchString(strings.TrimPrefix(asked[i], want)) {
			t.Errorf("verb %q, want %q followed by the session's hex credential", asked[i], want)
		}
	}
	if len(smb.mounted) != 2 || smb.mounted[0].host != "plug-mnt-data" || smb.mounted[0].share != "vol" || !hexPass.MatchString(smb.mounted[0].pass) {
		t.Errorf("mounted %+v, want the helper the agent named, with the minted credential", smb.mounted)
	}
	if runtime.GOOS != "windows" {
		if !strings.HasPrefix(smb.at[0], tmp) || !strings.HasSuffix(smb.at[0], filepath.Join("data")) || !strings.HasSuffix(smb.at[1], filepath.Join("logs")) {
			t.Errorf("mounted at %v, want under the session directory in %s at the cluster paths", smb.at, tmp)
		}
	}
	// The forward the OS client dials is this process's own loopback listener
	// where the OS needs one.
	if mountUsesForward() && !strings.HasPrefix(smb.mounted[0].local, "127.0.0.1:") {
		t.Errorf("the mount was pointed at %q, want a loopback forward", smb.mounted[0].local)
	}
	// The projection's view, and the record a crash would leave.
	vm := autoMountsFor("geo")
	if len(vm) != 2 || vm[0].cluster != "/data" || vm[0].local != smb.at[0] || vm[1].cluster != "/logs" {
		t.Errorf("autoMountsFor(geo) = %+v", vm)
	}
	vars := localizeVolumeEnv([]string{"DATA_DIR=/data/tiles", "LOG=/logs"}, vm)
	if vars[0] != "DATA_DIR="+filepath.Join(smb.at[0], "tiles") || vars[1] != "LOG="+smb.at[1] {
		t.Errorf("repointed as %v", vars)
	}
	recs := mountRecords()
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want one per mount", recs)
	}
	for _, r := range recs {
		if r.pid != os.Getpid() || r.cluster != "agent.example:2222" || !r.auto {
			t.Errorf("record %+v, want this pid, the cluster and auto", r)
		}
	}

	stop()
	if len(smb.unmounted) != 2 || smb.unmounted[0] != smb.at[1] || smb.unmounted[1] != smb.at[0] {
		t.Errorf("unmounted %v, want both, last mounted first", smb.unmounted)
	}
	if got := f.askedPrefixed("unmount-volume "); len(got) != 2 || got[0] != "unmount-volume geo /logs 40001" {
		t.Errorf("helpers released as %v", got)
	}
	if recs := mountRecords(); len(recs) != 0 {
		t.Errorf("records survived the teardown: %+v", recs)
	}
	if vm := autoMountsFor("geo"); vm != nil {
		t.Errorf("the projection still sees %+v after the teardown", vm)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("the session directory was not removed: %v", entries)
	}
	if f.closedTimes() != 1 {
		t.Errorf("transport closed %d times, want once", f.closedTimes())
	}
}

// --no-mount=/logs leaves that one out; --no-mount alone mounts nothing and
// dials nothing. An explicit --mount goes exactly where it was asked, and is
// not recorded as automatic.
func TestStartMountsHonoursThePolicyAndTheExplicitMounts(t *testing.T) {
	sandboxHome(t)
	sandboxTemp(t)
	smb := installFakeSMB(t)
	f := fakeMountSession(mountAgent)
	installFakeSession(t, f)
	at := filepath.Join(t.TempDir(), "srv", "data")
	cfg := config{host: "agent.example", port: "2222", exposes: exposeSpecs("geo:80:3000"),
		mountPolicy: parseNoMount("/logs"),
		mounts:      []mountSpec{{name: "geo", volume: "data", path: at}}}

	stop, err := startMounts(cfg)
	if err != nil {
		t.Fatalf("startMounts: %v", err)
	}
	defer stop()
	if got := f.askedPrefixed("mount-volume "); len(got) != 2 || !strings.HasPrefix(got[0], "mount-volume geo /data ") || !strings.HasPrefix(got[1], "mount-volume geo data ") {
		t.Errorf("asked %v, want /data (automatic, /logs dropped) then data (explicit)", got)
	}
	if runtime.GOOS != "windows" && (len(smb.at) != 2 || smb.at[1] != at) {
		t.Errorf("the explicit mount landed at %v, want %s", smb.at, at)
	}
	auto := 0
	for _, r := range mountRecords() {
		if r.auto {
			auto++
		}
	}
	if auto != 1 {
		t.Errorf("%d automatic record(s), want 1: the explicit mount is the person's", auto)
	}

	// Off entirely: nothing to do, nothing dialled.
	g := fakeMountSession(mountAgent)
	installFakeSession(t, g)
	off, err := startMounts(config{exposes: exposeSpecs("geo:80:3000"), mountPolicy: parseNoMount("")})
	if err != nil {
		t.Fatal(err)
	}
	off()
	if len(g.verbs()) != 0 {
		t.Errorf("--no-mount still asked the agent: %v", g.verbs())
	}
}

// A workload with no volume, or an agent before the verb, mounts nothing and
// holds no transport open for it.
func TestStartMountsWithNothingToMount(t *testing.T) {
	sandboxHome(t)
	sandboxTemp(t)
	installFakeSMB(t)
	for _, reply := range []string{"volumes", "error: unknown command \"volumes-of\""} {
		f := fakeMountSession(func(cmd string) (string, error) {
			if cmd == "volumes-of geo" {
				return reply, nil
			}
			return mountAgent(cmd)
		})
		installFakeSession(t, f)
		stop, err := startMounts(config{exposes: exposeSpecs("geo:80:3000")})
		if err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
		stop()
		if got := f.askedPrefixed("mount-volume "); len(got) != 0 {
			t.Errorf("%q: mounted %v", reply, got)
		}
		if f.closedTimes() != 1 {
			t.Errorf("%q: transport closed %d times, want once", reply, f.closedTimes())
		}
	}
}

// A mount that fails ends the session, and what was mounted before it is
// unmounted, its helper released and its record forgotten. The progress line
// of the failed one is closed with the failure, including when it is the
// local forward that could not be opened: that one used to be left spinning.
func TestStartMountsUndoesEverythingWhenOneMountFails(t *testing.T) {
	for _, c := range []struct {
		name    string
		bind    string
		smbErr  string
		wantErr string
	}{
		{"the OS refuses the mount", "127.0.0.1:0", "mount_smbfs: Authentication error", "Authentication error"},
		{"the forward cannot listen", "127.0.0.1:99999", "", "cannot listen"},
	} {
		if c.bind != "127.0.0.1:0" && !mountUsesForward() {
			continue // no forward on this OS outside --dockerrun
		}
		sandboxHome(t)
		sandboxTemp(t)
		smb := installFakeSMB(t)
		savedBind := mountForwardBind
		t.Cleanup(func() { mountForwardBind = savedBind })
		f := fakeMountSession(mountAgent)
		installFakeSession(t, f)
		// The first volume mounts; the second fails.
		mounts := 0
		realMount := mountSMB
		mountSMB = func(target mountTarget, path string) (string, error) {
			mounts++
			if mounts == 2 && c.smbErr != "" {
				return "", errStr(c.smbErr)
			}
			return realMount(target, path)
		}
		if c.bind != "127.0.0.1:0" {
			f.reply = func(cmd string) (string, error) {
				if cmd == "volumes-of geo" {
					return "volumes /data", nil
				}
				return mountAgent(cmd)
			}
			mountForwardBind = c.bind
		}
		lines := captureStderr(t)
		_, err := startMounts(config{host: "agent.example", port: "2222", exposes: exposeSpecs("geo:80:3000")})
		out := lines()
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
		if !strings.Contains(out, ": failed (") {
			t.Errorf("%s: the progress line was never closed with the failure:\n%s", c.name, out)
		}
		if len(smb.unmounted) != len(smb.at) {
			t.Errorf("%s: mounted at %v, unmounted %v", c.name, smb.at, smb.unmounted)
		}
		if recs := mountRecords(); len(recs) != 0 {
			t.Errorf("%s: records left by the failed session: %+v", c.name, recs)
		}
		if f.closedTimes() != 1 {
			t.Errorf("%s: transport closed %d times, want once", c.name, f.closedTimes())
		}
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }

// One record per mountpoint means one NAME per path. The name is the path in
// hex, cut to fit a file name, and the automatic mounts of one workload share
// a session-directory prefix that on macOS is longer than the cut: two
// volumes used to write the same record, and the sweep after a crash saw one.
func TestMountRecordNamesStayDistinctUnderALongSharedPrefix(t *testing.T) {
	prefix := strings.Repeat("plug-vol-session", 5) // past the cut on its own
	a := recordName(filepath.Join(prefix, "data"))
	b := recordName(filepath.Join(prefix, "logs"))
	if a == b {
		t.Fatalf("two mountpoints under one long prefix share a record name: %s", a)
	}
	if len(a) > 120 || len(b) > 120 {
		t.Errorf("record names are %d and %d long, want at most 120", len(a), len(b))
	}
	// A short path is still just its hex: what doctor and a person see.
	if short := recordName(filepath.Join(string(filepath.Separator), "data")); len(short) > 120 || short != recordName(filepath.Join(string(filepath.Separator), "data")) {
		t.Errorf("a short path's record name is not stable: %s", short)
	}
}
