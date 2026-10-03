package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/softwarity/plug/cli/internal/tunnel"
	"golang.org/x/crypto/ssh"

	"golang.org/x/term"
)

// safeVersionRe is what an agent version may contain before plug turns it into
// a directory name under ~/.plug/versions. Deliberately narrow: no separator,
// no dot-dot, nothing shell- or path-significant. Covers both shapes plug
// publishes — "2.5.4" and "dev+9f2a1c".
var safeVersionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)

func ensureVersion(v string, cfg config) (*os.File, error) {
	// v is whatever the AGENT answered to `version` — a remote string, and this
	// is the one place it becomes a filesystem path (and then an executable we
	// run, on macOS as root). filepath.Join cleans "..", it does not confine:
	// only this charset does. Releases are "2.5.4", branch builds "dev+9f2a1c".
	if !safeVersionRe.MatchString(v) {
		return nil, fmt.Errorf("the agent reports version %q, which is not a version plug will use as a cache path — "+
			"redeploy the softwarity/plug image", v)
	}
	dir := filepath.Join(versionsDir(), v)
	name := "plug"
	if runtime.GOOS == "windows" {
		name += ".exe" // Windows won't exec a versioned binary without the extension
	}
	bin := filepath.Join(dir, name)
	// Before anything reads or writes here, and not only before the write. This
	// guard is what stands in, on macOS, for the TOCTOU defence Linux gets from
	// /proc/self/fd: it refuses a store whose existing components are not
	// root-owned. It was called on the download path alone, so a cache HIT - the
	// common case, and the one that ends in exec'ing that file with privilege -
	// went through unguarded. The check the store rests on has to cover the path
	// that runs the binary, not just the path that writes it.
	guardStorePath(dir)
	// Hand the cache back to the user: the setuid helper writes it as euid 0, so
	// without this it lands root-owned (can't be listed/cleaned without sudo). Also
	// self-heals a cache an earlier privileged run already left root-owned.
	// Hand the cache back to the user — but only where it IS the user's: a store
	// that belongs to root is the point of the arrangement, not an accident to
	// be undone.
	own := func() {
		if storeIsSystem() {
			return
		}
		chownToUser(versionsDir())
		chownToUser(dir)
		chownToUser(bin)
	}
	osArch := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	// What the agent says this binary must hash to. Asked EVERY launch, because
	// the tamper we are guarding against happens after the download: the cached
	// core is executed with the privilege plug holds (root on macOS, ambient
	// caps on Linux), so anything able to rewrite it — a postinstall in the very
	// project plug is launching — would be running with it. ~30ms for 9MB.
	att, derr := fetchDigest(cfg, osArch)
	want := att.sha256
	if fi, err := os.Stat(bin); err == nil && fi.Size() > 1<<20 && derr == nil {
		f, herr := openVerified(bin, want)
		switch {
		case herr == nil:
			// openVerified proved the bytes on disk hash to want, so want IS the
			// measurement here, not a claim. The signature is checked against it
			// on every launch, not only on the download: the cache is what gets
			// executed with privilege, and it outlives the download by weeks.
			if serr := verifyCore(att, osArch, want); serr != nil {
				f.Close()
				return nil, serr
			}
			own()
			ensureWintunBeside(bin, cfg)
			return f, nil
		case errors.Is(herr, errCoreDigest):
			// A RELEASE version names one commit, so the same version can only
			// mean the same bytes: a mismatch there is corruption or tampering
			// and is worth saying out loud. A dev or branch build legitimately
			// covers different bytes over time — re-fetching is routine.
			if releaseVersionRe.MatchString(shortVersion(v)) {
				info("WARNING the cached v%s does not match what the agent serves — discarding it and fetching again.\n"+
					"      A published release names one build, so this is corruption or tampering, not a new version.", v)
			}
			_ = os.Remove(bin)
		}
	}
	// Decided before the download rather than after it, because there is nothing
	// a download could add: without a digest there is nothing to compare it to.
	if derr != nil {
		return nil, digestRefusal(derr, "v"+v)
	}
	data, err := getDownload(cfg, osArch, "v"+v)
	if err != nil {
		return nil, err
	}
	if len(data) < 1<<20 || !looksLikeBinary(data) {
		return nil, fmt.Errorf("downloaded binary looks invalid (%d bytes)", len(data))
	}
	got := fmt.Sprintf("%x", sha256.Sum256(data))
	// Before the bytes ever reach the disk. What follows writes them into a store
	// only root can touch and then executes them with privilege, so this is the
	// last point where refusing costs nothing. The same decision `plug update`
	// makes before overwriting the launcher (admitPrivilegedBytes), on purpose.
	if serr := admitPrivilegedBytes(att, nil, osArch, got, "v"+v); serr != nil {
		return nil, serr
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, ".plug-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Chmod(0o755)
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), bin); err != nil {
		return nil, err
	}
	own()
	ensureWintunBeside(bin, cfg)
	// Verified again, through a descriptor this time: the bytes were checked in
	// memory before the write, but what gets RUN is what is on disk now.
	return openVerified(bin, want)
}

// ensureWintunBeside copies wintun.dll next to a versioned binary on Windows.
// WinTUN's loader looks only in the executable's OWN directory (a hardening choice,
// not the PATH), so a binary run from ~/.plug/versions/<v>/ can't find the wintun.dll
// the installer dropped in the launcher dir ("Error loading wintun.dll ... module
// could not be found"). Best-effort: copy it from beside the launcher; no-op elsewhere.
//
// Copied only once the agent has vouched for it (admitWintun): the file beside
// the launcher sits in a directory only administrators write, but what is
// written here is loaded by a process plug starts, and the rule is one rule.
// An agent that cannot attest the driver leaves the versioned directory
// without one, and says so once rather than copying on faith.
func ensureWintunBeside(bin string, cfg config) {
	if runtime.GOOS != "windows" {
		return
	}
	dst := filepath.Join(filepath.Dir(bin), "wintun.dll")
	if _, err := os.Stat(dst); err == nil {
		return // already there
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(self), "wintun.dll"))
	if err != nil {
		return // launcher has none beside it, nothing to copy
	}
	if err := admitWintun(cfg, data); err != nil {
		info("wintun.dll is not copied next to the core: %v", err)
		return
	}
	if err := writeWintun(filepath.Dir(bin), data); err != nil {
		info("wintun.dll could not be copied next to the core: %v", err)
	}
}

// cmdVersion prints THIS launcher's version — or, pointed at a cluster
// (-p/-H), that cluster AGENT's version. One bare value either way, so scripts
// stay trivial; `plug versions` is the whole picture at once. The bare form
// must stay network-free and instant: installers and doctor exec it.
func cmdVersion(args []string) {
	var profile, host, port string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			profile = flagValue(args, &i)
		case "-H", "--host":
			host = flagValue(args, &i)
		case "--port":
			port = flagValue(args, &i)
		default:
			fatal("usage: plug version [-p profile]")
		}
	}
	if profile == "" && host == "" {
		fmt.Println(version)
		warnStaleLauncher()
		return
	}
	cfg, _ := updateTarget(profile, host, port)
	v, err := agentVersion(cfg)
	if err != nil {
		fatal("cannot reach the agent at %s:%s: %v", cfg.host, cfg.port, err)
	}
	fmt.Println(shortVersion(v))
}

// warnStaleLauncher — bare `plug version` answers for the LAUNCHER, which is
// not what sessions run (they run each cluster's exact core). With clusters
// freshly updated that bare answer reads like plug is old — disorienting. When
// the LOCAL CACHE proves a cluster already served a newer release, say so: on
// stderr, tty only (stdout stays a bare value — scripts and doctor exec it),
// and never over the network (version's instant/offline contract).
func warnStaleLauncher() {
	if !isTTY(os.Stderr) || !semverOK(version) {
		return // piped, or a dev build — a developer, not the audience
	}
	if max := maxCachedRelease(); max != "" && semverLess(version, max) {
		info("a cluster already served v%s — this launcher is v%s; plug update aligns it", shortVersion(max), shortVersion(version))
	}
}

// maxCachedRelease is the newest RELEASED core in ~/.plug/versions ("" when
// only dev builds are cached).
func maxCachedRelease() string {
	entries, err := os.ReadDir(versionsDir())
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() && semverOK(e.Name()) && (best == "" || semverLess(best, e.Name())) {
			best = e.Name()
		}
	}
	return best
}

// agentVersionTimeout bounds agentVersion for the parallel `plug versions`
// sweep — a down cluster must cost seconds, not the full dial timeout.
func agentVersionTimeout(cfg config, d time.Duration) (string, error) {
	type res struct {
		v   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := agentVersion(cfg)
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(d):
		return "", errors.New("timeout")
	}
}

func listVersions() {
	fmt.Printf("launcher: v%s\n", shortVersion(version))
	entries, err := os.ReadDir(versionsDir())
	var cached []string
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				cached = append(cached, e.Name())
			}
		}
		sort.Strings(cached)
	}
	if len(cached) == 0 {
		fmt.Println("cached: (none yet)")
	} else {
		fmt.Printf("cached: %s\n", strings.Join(cached, ", "))
	}
	// Each profile's agent, asked in parallel — the answer to "which version is
	// each of my clusters on?", not just what happens to be cached locally.
	names := listProfiles()
	if len(names) == 0 {
		return
	}
	rows := make(map[string]string, len(names))
	type row struct{ name, out string }
	ch := make(chan row, len(names))
	for _, n := range names {
		go func(n string) {
			cfg, err := readProfileSoft(n)
			if err != nil {
				ch <- row{n, "broken profile (" + err.Error() + ")"}
				return
			}
			v, err := agentVersionTimeout(cfg, 5*time.Second)
			if err != nil {
				ch <- row{n, fmt.Sprintf("unreachable (%s:%s)", cfg.host, cfg.port)}
				return
			}
			ch <- row{n, fmt.Sprintf("agent v%s (%s:%s)", shortVersion(v), cfg.host, cfg.port)}
		}(n)
	}
	for range names {
		r := <-ch
		rows[r.name] = r.out
	}
	for _, n := range names {
		fmt.Printf("%s: %s\n", n, rows[n])
	}
}

// ---- get-user helpers (no key: passwordless ForceCommand download) ----

// dialGetUser opens an SSH connection to the agent as the anonymous `get` user,
// using the crypto/ssh library rather than the platform ssh binary.
//
// Why the library: on Windows, shelling out to ssh and capturing its stdout over
// a pipe hangs — OpenSSH forks a child that holds the pipe's write end open past
// ssh's own exit, so the read never sees EOF. crypto/ssh reads the channel
// directly, with no external process or OS pipe, so it behaves the same on every
// platform.
//
// The get user authenticates with "none" (AuthenticationMethods none on the
// agent): there is no client secret to protect here, and requiring a key to
// fetch the thing that holds the key is a circle.
//
// The HOST key is another matter, and this used to ignore it outright, on the
// grounds that pinning lived on the data tunnel. That reasoning had the channels
// backwards. This one carries the version, the DIGEST that version must hash to,
// and the BINARY itself, which is then executed as root on macOS and with
// ambient CAP_SYS_ADMIN on Linux. Someone able to answer here supplies a
// coherent digest and binary, openVerified agrees they match, and the result
// runs with privilege. "Whoever reaches the agent is trusted" was never meant to
// mean "whoever is on the path can hand this machine root".
//
// Same policy and the same file as the tunnel: first use records the key, a
// change re-pins it and says so, because the agent legitimately regenerates its
// key when its container is recreated. That is detection rather than prevention,
// and it is what an agent whose identity does not survive a redeploy can carry.
// Blocking would fail every session after a routine redeploy.
func dialGetUser(cfg config) (*ssh.Client, error) {
	addr := net.JoinHostPort(cfg.host, cfg.port)
	pin := knownHostsFor(cfg.host)
	if pin != "" {
		// Same two steps dialTunnel takes around the same file, and skipping
		// either broke every macOS session: this dial runs FIRST (it is how the
		// version is asked), so it is the one that CREATES the pin file - as euid
		// 0, since the launcher is setuid. dialTunnel then found it owned by root
		// and refused it, correctly, with "a file outside your own tree".
		//
		// Guard before, hand back after. Deferred rather than conditional on
		// success: the callback writes during the handshake, so a dial that fails
		// afterwards still leaves a root-owned file behind.
		guardUserPath(pin)
		defer chownToUser(pin)
		tunnel.SetKnownHostsOwner(realUID()) // same account rule as dialTunnel
	}
	// tunnel.DialSSH, never ssh.Dial: ClientConfig.Timeout bounds the TCP connect
	// and NOTHING after it, so an agent that accepts the connection and then goes
	// quiet used to hold this dial for ever. This is the FIRST thing every launch
	// does - it is how the agent's version is asked - so it hung before plug had
	// printed a single line, and what the person saw was a command that never
	// started. See DialSSH for the whole story.
	return tunnel.DialSSH(addr, &ssh.ClientConfig{
		User:            getUser,
		HostKeyCallback: tunnel.HostKeyCallback(pin, addr, info),
	}, agentDialTimeout)
}

// How long the launcher waits on the agent, and the two numbers are different
// kinds of thing.
//
// A verb answers in milliseconds: it is a ForceCommand printing a line. Thirty
// seconds is not a budget, it is the point past which the agent is not slow, it
// is not answering. The core is nine megabytes over whatever link the person
// has, so its ceiling is generous on purpose - ten minutes is 15 kB/s, which no
// working link is under, and it exists only so that a transfer which has truly
// stopped ends in a sentence rather than in silence.
const (
	agentDialTimeout  = 15 * time.Second
	agentVerbTimeout  = 30 * time.Second
	agentFetchTimeout = 10 * time.Minute
)

// bounded runs f and gives up after d, CLOSING the client to get there.
//
// An established SSH session has no deadline of its own: its channel is a stream
// over a mux, and SetReadDeadline does not exist on it (the same reason expose.go
// gives). So there is no polite way to bound a read that will never end - closing
// the connection underneath it is what unblocks f, which then returns an error
// nobody is left to read.
//
// This matters because a dial that succeeds proves the agent answered a
// handshake, never that it will answer anything else. Both of these run before
// the person's command does, and a launcher that waits for ever on either one is
// indistinguishable, from the outside, from a command that silently did nothing.
// Takes an io.Closer rather than an *ssh.Client: closing is the whole of what it
// needs, and a rule about giving up is one that has to be provable without
// standing up an SSH server to prove it.
func bounded(c io.Closer, d time.Duration, what string, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		c.Close()
		return fmt.Errorf("the agent accepted the connection but never answered %s within %v", what, d)
	}
}

func agentVersion(cfg config) (string, error) { return getExec(cfg, "version") }

// getExec runs one verb on the agent's download channel and returns its output.
// The `get` user has a ForceCommand, so the verb is all it will ever run.
func getExec(cfg config, verb string) (string, error) {
	client, err := dialGetUser(cfg)
	if err != nil {
		return "", err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out []byte
	err = bounded(client, agentVerbTimeout, "`"+verb+"`", func() error {
		var e error
		out, e = sess.Output(verb)
		return e
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// updateHold keeps the "updated" line on screen briefly before the child runs
// (the child often clears the screen right away). Overridable in tests.
var updateHold = 400 * time.Millisecond

// minBarDuration keeps the bar animating for at least this long even when the
// transfer is near-instant (the usual case on a LAN) — the whole point is that
// the user actually sees the update happen. Overridable in tests.
var minBarDuration = 2 * time.Second

// fileSHA256 hashes a file on disk, streaming it — the core is ~9MB and there is
// no reason to hold a second copy in memory to check it.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return readerSHA256(f)
}

func readerSHA256(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// errCoreDigest: the bytes are not the ones the agent announced.
var errCoreDigest = errors.New("the cached core does not hash to what the agent serves")

// openVerified opens bin and hashes WHAT THE DESCRIPTOR HOLDS, then hands that
// descriptor on. Every caller runs the descriptor, never the path.
//
// The distinction is the whole point. Verifying a path and then executing that
// path leaves a gap between the two, and the core is executed with the privilege
// plug holds — root on macOS, ambient capabilities on Linux. Whatever can write
// into the cache during that gap runs with it, and what can write there is
// anything running as the user: the postinstall of the very project plug is
// launching, say. A descriptor is bound to an inode, so a file swapped in at
// that path afterwards is a different file, not this one.
func openVerified(bin, want string) (*os.File, error) {
	f, err := os.Open(bin)
	if err != nil {
		return nil, err
	}
	got, err := readerSHA256(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if got != want {
		f.Close()
		return nil, errCoreDigest
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// fetchDigest is the digest lookup, indirected so a test can exercise the cache
// paths without a cluster.
var fetchDigest = getDigest

// getDigest asks the agent what the binary for osArch must hash to.
//
// The answer travels the same channel the binary itself came from, and that
// channel now records the agent's host key (TOFU, ~/.plug/known_hosts, see
// dialGetUser) where it used to ignore it. Be exact about what that buys: the
// key is recorded on first use and a change is re-pinned WITH A NOTICE, so a
// substitution is noticed rather than prevented. What the digest proves is that
// the copy on disk is still the one that arrived, not that the source was
// honest. A forged AGENT would announce a matching hash for a
// forged binary — that is the threat a signature answers, and it is a separate
// layer this one is shaped to accept later.
//
// key=value lines, so a later `sig=` costs nothing here.
func getDigest(cfg config, osArch string) (coreAttestation, error) {
	out, err := getExec(cfg, "digest "+osArch)
	if err != nil {
		return coreAttestation{}, err
	}
	return parseAttestation(out, osArch)
}

// parseAttestation reads the digest verb's key=value reply. Split out from the
// fetch so a test can run a REAL agent's answer through it: what the image
// serves and what the launcher expects are two halves of one protocol, and the
// only way to know they still meet is to feed one to the other.
func parseAttestation(out, osArch string) (coreAttestation, error) {
	var att coreAttestation
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "sha256="); ok {
			if len(v) != 64 {
				return coreAttestation{}, fmt.Errorf("agent answered a malformed sha256 (%d chars)", len(v))
			}
			att.sha256 = v
		}
		// An agent older than release signing answers no sig line at all. That is
		// not an error here: verifyCore is what decides whether a missing
		// signature is still acceptable, and it is the only place that decides.
		if v, ok := strings.CutPrefix(line, "sig="); ok {
			att.sig = v
		}
	}
	if att.sha256 == "" {
		return coreAttestation{}, fmt.Errorf("agent answered no sha256 for %s", osArch)
	}
	return att, nil
}

// getDownload is the var the product calls, getDownloadFromAgent the body: a
// seam, so the launcher's replacement policy can be tested with bytes a test
// made rather than a cluster. Never reassigned outside tests.
var getDownload = getDownloadFromAgent

// getDownloadFromAgent streams a binary from the get-user over SSH. When stderr is a
// terminal it animates a progress bar so a version update is actually visible —
// the transfer is quick and the child usually wipes the screen right after.
// label is the version being fetched, for the display.
func getDownloadFromAgent(cfg config, osArch, label string) ([]byte, error) {
	client, err := dialGetUser(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	if err := sess.Start(osArch); err != nil {
		return nil, err
	}
	var data []byte
	rerr := bounded(client, agentFetchTimeout, "the "+label+" download", func() error {
		var e error
		data, e = readWithProgress(stdout, label, isTTY(os.Stderr))
		return e
	})
	if werr := sess.Wait(); werr != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("%s", s)
		}
		return nil, werr
	}
	return data, rerr
}

// readWithProgress reads r to EOF. When animate is set it draws an indeterminate
// progress bar + byte count on stderr and holds the final line briefly;
// otherwise it prints one plain line. The total size is unknown (the agent just
// streams the binary), hence an indeterminate bar rather than a percentage.
func readWithProgress(r io.Reader, label string, animate bool) ([]byte, error) {
	if !animate {
		fmt.Fprintf(os.Stderr, "[plug] downloading %s from the cluster...\n", label)
	}
	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	var frame int
	var last time.Time
	start := time.Now()
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if animate && time.Since(last) > 60*time.Millisecond {
				drawBar(label, int64(buf.Len()), frame)
				frame++
				last = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return buf.Bytes(), err
		}
	}
	if animate {
		// Keep the bar visible for a minimum duration even when the transfer was
		// near-instant — otherwise the update just flashes by unseen.
		for time.Since(start) < minBarDuration {
			drawBar(label, int64(buf.Len()), frame)
			frame++
			time.Sleep(70 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "\r[plug] ✓ updated to %s  (%s)%s\n",
			label, humanBytes(int64(buf.Len())), strings.Repeat(" ", 14))
		time.Sleep(updateHold)
	} else {
		fmt.Fprintf(os.Stderr, "[plug] updated to %s (%s)\n", label, humanBytes(int64(buf.Len())))
	}
	return buf.Bytes(), nil
}

// drawBar renders one frame of an indeterminate progress bar (a block bouncing
// left↔right), followed by the bytes read so far.
func drawBar(label string, n int64, frame int) {
	const w = 16
	pos := frame % (2 * (w - 1))
	if pos >= w {
		pos = 2*(w-1) - pos
	}
	var b strings.Builder
	for i := 0; i < w; i++ {
		if i >= pos-1 && i <= pos+1 {
			b.WriteRune('█')
		} else {
			b.WriteRune('░')
		}
	}
	fmt.Fprintf(os.Stderr, "\r[plug] updating %s  [%s]  %s ", label, b.String(), humanBytes(n))
}

// isTTY asks the OS, not the file mode: /dev/null is a character device too,
// and `plug update </dev/null` from a script used to pass this test and run
// sudo on a terminal nobody was at. Same rule as openTerminal.
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func looksLikeBinary(data []byte) bool {
	magics := [][]byte{
		{0x7f, 'E', 'L', 'F'},    // linux
		{0xcf, 0xfa, 0xed, 0xfe}, // macOS 64-bit mach-o
		{0xca, 0xfe, 0xba, 0xbe}, // macOS universal
		{'M', 'Z'},               // windows
	}
	for _, m := range magics {
		if bytes.HasPrefix(data, m) {
			return true
		}
	}
	return false
}
