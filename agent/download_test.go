package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeDownloadDir builds what /opt/plug holds in the image, small: binaries of
// sizes that land on and around base64's 57-byte line, one past the encoder's
// read buffer, one platform missing, signatures on some files and not others.
func fakeDownloadDir(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	rng := rand.New(rand.NewSource(1))
	put := func(name string, size int) {
		t.Helper()
		b := make([]byte, size)
		rng.Read(b)
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	text := func(name, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("bin/plug-linux-amd64", 57*1024*2+5)
	put("bin/plug-linux-arm64", 57)
	put("bin/plug-darwin-arm64", 1)
	put("bin/plug-windows-amd64.exe", 114)
	put("bin/plug-windows-arm64.exe", 3000)
	put("wintun.dll", 4000)
	put("wintun-arm64.dll", 4001)
	text("bin/plug-linux-amd64.sig", "c2lnbmF0dXJlLWxpbnV4\n")
	text("bin/plug-windows-arm64.exe.sig", "c2lnbmF0dXJlLXdpbmRvd3M=")
	text("wintun.dll.sig", "c2lnbmF0dXJlLWRsbA==\n\n")
	text("VERSION", version)
	text("install.sh", "# the windows installer\nexit 0\n")
	return dir
}

// The reference must run where it ran in the image, or close: a shell whose
// echo leaves backslashes alone in what these cases print, and a base64 that
// wraps at 76 columns as busybox and coreutils do. macOS's wraps nothing, and
// Windows has no sh: the comparison runs on Linux, in CI's build job.
func referenceShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/sh to run the reference script")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	out, err := exec.Command(sh, "-c", "head -c 100 /dev/zero | base64").Output()
	if err != nil || strings.Count(string(out), "\n") != 2 {
		t.Skip("this base64 does not wrap at 76 columns as the image's did; the comparison runs on Linux")
	}
	return sh
}

type downloadAnswer struct {
	stdout, stderr string
	code           int
}

func runReference(t *testing.T, sh, script, cmd, key string) downloadAnswer {
	t.Helper()
	c := exec.Command(sh, script)
	// The agent's own environment carries no locale, so the script counted
	// bytes and judged characters in the C locale.
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "SSH_ORIGINAL_COMMAND=" + cmd, keyInstructionEnv + "=" + key}
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	code := 0
	if err := c.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("the reference did not run: %v", err)
		}
		code = ee.ExitCode()
	}
	return downloadAnswer{stdout.String(), stderr.String(), code}
}

func runDownload(cmd, key, dir string) downloadAnswer {
	var stdout, stderr bytes.Buffer
	code := serveDownload(cmd, key, dir, &stdout, &stderr)
	return downloadAnswer{stdout.String(), stderr.String(), code}
}

// The script points at its own copy of the fake /opt/plug.
func referenceScript(t *testing.T, dir string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "serve-binary.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte("/opt/plug")) {
		t.Fatal("the frozen script no longer names /opt/plug")
	}
	src = withTodaysInstaller(t, src)
	path := filepath.Join(t.TempDir(), "serve-binary")
	if err := os.WriteFile(path, bytes.ReplaceAll(src, []byte("/opt/plug"), []byte(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// withTodaysInstaller puts install-unix.sh as it is today where the frozen
// script carries the installer it had then: its two heredocs, HEAD and TAIL,
// are the halves of that file around binariesMarker, which they were byte for
// byte when it was frozen. The installer is read by sh on the developer's side
// and evolves with plug; what the reference pins is everything around it (the
// variables, the binaries, every verb and refusal), which no launcher must see
// change. The frozen file itself stays as it was.
func withTodaysInstaller(t *testing.T, src []byte) []byte {
	t.Helper()
	head, tail, ok := strings.Cut(unixInstaller, binariesMarker)
	if !ok {
		t.Fatal("install-unix.sh no longer carries the binaries marker")
	}
	for _, h := range []struct{ tag, body string }{{"HEAD", head}, {"TAIL", tail}} {
		open := []byte("cat <<'" + h.tag + "'\n")
		i := bytes.Index(src, open)
		if i < 0 {
			t.Fatalf("the frozen script no longer has its %s heredoc", h.tag)
		}
		start := i + len(open)
		end := bytes.Index(src[start:], []byte("\n"+h.tag+"\n"))
		if end < 0 {
			t.Fatalf("the frozen script's %s heredoc has no end", h.tag)
		}
		body := strings.TrimSuffix(h.body, "\n")
		src = append(append(append([]byte{}, src[:start]...), body...), src[start+end:]...)
	}
	return src
}

// The account answers what the shell script answered, byte for byte: stdout,
// stderr and exit code, for every verb, every refusal and every trap the
// script guarded against. Launchers already installed parse these answers.
func TestDownloadAnswersAsTheScriptDid(t *testing.T) {
	sh := referenceShell(t)
	sentence := "register the key it prints in your dev profile"
	commands := []string{
		"", "   ", "\t\n", "version", "VERSION", "Version and more words",
		"install", "INSTALL", "install canopy", "install Canopy", "install canopy-hh.dev_1",
		"install 0", "install " + strings.Repeat("a", 63), "install " + strings.Repeat("a", 64),
		"install ../x", "install a;rm", "install -x", "install .x", "install _x", "install é", "install *",
		"install\tcanopy", "install canopy extra",
		"install-windows", "install-windows canopy", "install-windows ../x", "install.sh", "install.sh Canopy",
		"wintun", "wintun-amd64", "wintun-arm64", "WINTUN-ARM64", "wintun-x86",
		"digest linux-amd64", "digest linux-x86_64", "digest Linux-AARCH64", "digest linux-arm64",
		"digest darwin-arm64", "digest darwin-amd64", "digest windows-amd64", "digest windows-arm64",
		"digest windows-x86_64", "digest wintun", "digest wintun-amd64", "digest wintun-arm64",
		"digest", "digest linux", "digest windows", "digest ../VERSION", "digest linux-amd64/..",
		"digest x-../../VERSION", "digest *", "digest linux-amd64.sig",
		"linux-amd64", "linux-x86_64", "LINUX-AARCH64", "darwin-arm64", "darwin-amd64",
		"windows-amd64", "windows-arm64", "windows-x86_64", "freebsd-amd64", "linux", "-", "-x",
		"*", "* *", "?", "unknown", "\tversion\n", "x-../VERSION", "bin/plug-linux-amd64",
	}
	for _, version := range []string{"2.24.0", "2.24.0-hosted", "2.24.0+abc1234-hosted\n"} {
		dir := fakeDownloadDir(t, version)
		script := referenceScript(t, dir)
		for _, cmd := range commands {
			for _, key := range []string{"", sentence} {
				want := runReference(t, sh, script, cmd, key)
				if got := runDownload(cmd, key, dir); got != want {
					t.Errorf("VERSION %q, command %q, key %q:\n%s", version, cmd, key, diffAnswers(want, got))
				}
			}
		}
	}
}

// What the gateway says about the key is written inside single quotes in a
// script the developer runs. The script's rules decide what is said.
func TestDownloadSaysTheKeySentenceAsTheScriptDid(t *testing.T) {
	sh := referenceShell(t)
	dir := fakeDownloadDir(t, "2.24.0-hosted")
	script := referenceScript(t, dir)
	keys := []string{
		"register the key it prints in your dev profile",
		"it's yours", `a\b`, "a\nb", "a\rb", "a\tb", "bell\a", "del\x7f", "trailing\n",
		strings.Repeat("a", 200), strings.Repeat("a", 201),
		strings.Repeat("é", 100), strings.Repeat("é", 101),
		`50% done %s $HOME $(id) "quoted" ; | &`,
	}
	for _, cmd := range []string{"install canopy", "install-windows canopy"} {
		for _, key := range keys {
			want := runReference(t, sh, script, cmd, key)
			if got := runDownload(cmd, key, dir); got != want {
				t.Errorf("command %q, key %q:\n%s", cmd, key, diffAnswers(want, got))
			}
		}
	}
}

func diffAnswers(want, got downloadAnswer) string {
	var b strings.Builder
	if want.code != got.code {
		fmt.Fprintf(&b, "  exit %d, want %d\n", got.code, want.code)
	}
	if want.stderr != got.stderr {
		fmt.Fprintf(&b, "  stderr %q\n  want   %q\n", got.stderr, want.stderr)
	}
	if want.stdout != got.stdout {
		fmt.Fprintf(&b, "  stdout %s\n  want   %s\n", firstDifference(got.stdout, want.stdout), firstDifference(want.stdout, got.stdout))
	}
	return b.String()
}

// firstDifference shows a window around where a and b part, as long installers
// make a whole-string dump useless.
func firstDifference(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo, hi := max(0, i-40), min(len(a), i+40)
	return fmt.Sprintf("(%d bytes) at %d: %q", len(a), i, a[lo:hi])
}

// The binaries in the installer decode to the files served, in lines of 76
// columns or fewer. Holds on every OS, where the comparison above runs on
// Linux only.
func TestTheInstallerCarriesTheBinariesWhole(t *testing.T) {
	dir := fakeDownloadDir(t, "2.24.0")
	got := runDownload("install", "", dir)
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("install: exit %d, %q", got.code, got.stderr)
	}
	blocks := map[string]string{}
	var name string
	var body strings.Builder
	sc := bufio.NewScanner(strings.NewReader(got.stdout))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "emit_") && strings.HasSuffix(line, "() { decode <<'B64'"):
			name = strings.TrimSuffix(strings.TrimPrefix(line, "emit_"), "() { decode <<'B64'")
			body.Reset()
		case line == "B64" && name != "":
			blocks[name] = body.String()
			name = ""
		case name != "":
			if len(line) > 76 {
				t.Fatalf("emit_%s: a line of %d columns", name, len(line))
			}
			body.WriteString(line)
		}
	}
	want := map[string]string{"linux_amd64": "linux-amd64", "linux_arm64": "linux-arm64", "darwin_arm64": "darwin-arm64"}
	if len(blocks) != len(want) {
		t.Fatalf("the installer embeds %d binaries, want %d (darwin-amd64 is absent from this agent)", len(blocks), len(want))
	}
	for fn, file := range want {
		raw, err := base64.StdEncoding.DecodeString(blocks[fn])
		if err != nil {
			t.Fatalf("emit_%s: %v", fn, err)
		}
		served, _ := os.ReadFile(filepath.Join(dir, "bin", "plug-"+file))
		if !bytes.Equal(raw, served) {
			t.Fatalf("emit_%s decodes to %d bytes that are not plug-%s (%d)", fn, len(raw), file, len(served))
		}
	}
	if !strings.HasPrefix(got.stdout, "#!/bin/sh\n_name=\n_keyhint=''\n_hosted=\n#!/bin/sh\nset -e\n") {
		t.Fatalf("the installer starts %q", got.stdout[:60])
	}
	if strings.Contains(got.stdout, binariesMarker) {
		t.Fatal("the marker where the binaries go was emitted")
	}
}

// A refusal emits no script at all: whatever the developer piped it into
// runs nothing.
func TestARefusedProfileNameEmitsNoScript(t *testing.T) {
	dir := fakeDownloadDir(t, "2.24.0")
	for _, cmd := range []string{"install ../x", "install-windows a;rm", `install a\b`} {
		got := runDownload(cmd, "", dir)
		if got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, "is not a profile name") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", cmd, got.code, got.stdout, got.stderr)
		}
	}
}

// The digest is the file's, and nothing outside bin/ and the DLLs is reached,
// whatever the request spells.
func TestDigestNamesOnlyWhatIsServed(t *testing.T) {
	dir := fakeDownloadDir(t, "2.24.0")
	served, _ := os.ReadFile(filepath.Join(dir, "bin", "plug-linux-amd64"))
	got := runDownload("digest linux-x86_64", "", dir)
	want := fmt.Sprintf("sha256=%x\nsig=c2lnbmF0dXJlLWxpbnV4\n", sha256.Sum256(served))
	if got.code != 0 || got.stdout != want {
		t.Fatalf("digest: exit %d, %q, want %q", got.code, got.stdout, want)
	}
	for _, cmd := range []string{"digest ../VERSION", "digest linux-amd64/../plug-linux-arm64", "digest x-../../VERSION", "x-../../VERSION", "linux-amd64/../plug-linux-arm64"} {
		if got := runDownload(cmd, "", dir); got.code != 1 || got.stdout != "" {
			t.Errorf("%q answered %q (exit %d)", cmd, got.stdout, got.code)
		}
	}
}

func TestBinaryNameAcceptsUnameSpellings(t *testing.T) {
	for in, want := range map[string]string{
		"linux-x86_64":   "linux-amd64",
		"linux-aarch64":  "linux-arm64",
		"darwin-arm64":   "darwin-arm64",
		"windows-amd64":  "windows-amd64.exe",
		"windows-x86_64": "windows-amd64.exe",
		"linux":          "linux-linux",
		"":               "-",
		"a-b-c":          "a-b-c",
	} {
		if got := binaryName(in); got != want {
			t.Errorf("binaryName(%q) = %q, want %q", in, got, want)
		}
	}
}

// An agent that admits by approval tells its installer so, and the installer
// makes the first contact. An open agent's installer starts exactly as before:
// the variable is absent, not empty.
func TestTheInstallerKnowsTheAgentAdmitsByApproval(t *testing.T) {
	dir := fakeDownloadDir(t, "2.26.0")
	t.Setenv(admissionEnv, "")
	if got := runDownload("install demo", "", dir); strings.Contains(got.stdout, "_admission=") {
		t.Errorf("an open agent's installer carries the admission variable")
	}
	t.Setenv(admissionEnv, "invite")
	got := runDownload("install demo", "", dir)
	if !strings.HasPrefix(got.stdout, "#!/bin/sh\n_name=demo\n_keyhint=''\n_hosted=\n_admission=invite\n#!/bin/sh\n") {
		t.Fatalf("an invite agent's installer starts:\n%.120s", got.stdout)
	}
	if !strings.Contains(got.stdout, `"$dest/plug" -p "$_pname" requests </dev/null`) {
		t.Error("the installer does not make the first contact")
	}
}
