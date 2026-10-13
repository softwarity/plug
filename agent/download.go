package agent

// The anonymous `get` account, whole. It hands out what /opt/plug holds and
// nothing else: the version, the installers, the client binaries, the WinTUN
// DLLs and their digests.
//
// It was a /bin/sh script, agent/serve-binary, and that script was the only
// reason the image carried a shell and busybox. The account has no
// authentication by design, so the command it parses comes from anyone who
// reaches the port: the script kept that safe with `set -f` and a validation per
// value. Here there is no shell to keep safe, and no interpreter in the image
// for a mistake to reach.
//
// It answers exactly what the script answered, byte for byte, and
// download_test.go holds it to that against the script itself (frozen in
// testdata). What it says is a protocol: launchers already installed parse the
// `digest` lines and the installer it emits, so a byte changed here is a
// change for clients that cannot be updated with it.
//
// It runs in a process of its own, like the verbs: the agent re-executes its
// command for the account (Config.DownloadCommand, by default the verbs' own),
// and accountEnv tells that process it answers for `get`. One binary, so an
// embedder serves the account with what it already runs, and copies no script.

import (
	"bufio"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// downloadDir is what the account hands out. An embedder copies it wholesale
// from plug's image, which is why nothing that is the agent's own state lives
// there (see stateDir).
const downloadDir = "/opt/plug"

// accountEnv names the account a re-executed agent answers for. Set by the
// server for every command it runs, last, so it overrides whatever the agent's
// own environment says: the client cannot send environment variables (the
// server refuses every "env" request), so the value is always the server's.
const accountEnv = "PLUG_ACCOUNT"

// unixInstaller is the script `install` emits for Linux and macOS, binaries
// excepted: they go where binariesMarker stands, one emit_<os>_<arch> function
// each. Client-side shell, run by the developer, so it is kept as a script
// rather than rebuilt from Go strings.
//
//go:embed install-unix.sh
var unixInstaller string

const binariesMarker = "# @@ the emit_<os>_<arch> functions, one per binary, go here @@\n"

// The binaries `install` embeds, in the order the installer lists them.
var unixBinaries = []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"}

// The rule plug applies to every profile name (cli/profiles_cmd.go).
var profileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// serveDownload answers one command of the download account: original is
// SSH_ORIGINAL_COMMAND as the client sent it, keyInstruction what the embedder
// said about the key (Config.KeyInstruction), dir the directory handed out.
// It returns the process's exit code.
func serveDownload(original, keyInstruction, dir string, stdout, stderr io.Writer) int {
	out := bufio.NewWriterSize(stdout, 64<<10)
	defer out.Flush()

	// Lower case, then words, split on the shell's own separators and nothing
	// else: no globbing to drop here, since nothing expands.
	words := strings.FieldsFunc(asciiLower(original), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n'
	})
	word := func(i int) string {
		if i < len(words) {
			return words[i]
		}
		return ""
	}
	verb, arg := word(0), word(1)

	switch verb {
	case "version":
		return sendFile(out, stderr, dir+"/VERSION")

	// wintun answers the AMD64 DLL and always will: every Windows launcher
	// already installed asks for it by that bare name, and an architecture-
	// dependent answer would hand an arm64 driver to an amd64 machine on its
	// next update. Windows arm64 asks for wintun-arm64, the shape of
	// <os>-<arch> below.
	case "wintun", "wintun-amd64", "wintun-arm64":
		name := "wintun.dll"
		if verb == "wintun-arm64" {
			name = "wintun-arm64.dll"
		}
		if !isRegular(dir + "/" + name) {
			fmt.Fprintf(stderr, "plug: %s missing from this agent image\n", name)
			return 1
		}
		return sendFile(out, stderr, dir+"/"+name)

	// The Git Bash installer. It downloads the exe and the DLL over ssh rather
	// than embedding them, and picks the architecture from what Windows
	// reports rather than from uname, which lies under emulation:
	//   ssh get@<host> install-windows | bash -s -- <host> <port>
	case "install-windows", "install.sh":
		if !isRegular(dir + "/install.sh") {
			fmt.Fprintln(stderr, "plug: install.sh missing from this agent image")
			return 1
		}
		vars, ok := installVars("#!/bin/bash", arg, keyInstruction, dir, stderr)
		if !ok {
			return 1
		}
		out.WriteString(vars)
		return sendFile(out, stderr, dir+"/install.sh")

	// An installer with every unix binary embedded: it runs uname on the
	// developer's side to pick one, so the command stays a bare
	// `ssh get@host install | sh`. The second word is the profile to save.
	case "install":
		vars, ok := installVars("#!/bin/sh", arg, keyInstruction, dir, stderr)
		if !ok {
			return 1
		}
		head, tail, _ := strings.Cut(unixInstaller, binariesMarker)
		out.WriteString(vars)
		out.WriteString(head)
		for _, b := range unixBinaries {
			f := dir + "/bin/plug-" + b
			if !isRegular(f) {
				continue
			}
			fmt.Fprintf(out, "emit_%s() { decode <<'B64'\n", strings.Replace(b, "-", "_", 1))
			if err := sendBase64(out, f); err != nil {
				fmt.Fprintf(stderr, "plug: %v\n", err)
				return 1
			}
			out.WriteString("B64\n}\n")
		}
		out.WriteString(tail)
		return 0

	// The SHA-256 of what this agent would serve, so the launcher can check the
	// copy it cached is still the one it downloaded, and the release signature
	// over it when the image was built with the release key: the digest is
	// announced by this agent, and an agent is whoever the caller pointed plug
	// at. key=value lines, so a field can be added without breaking a parser.
	case "digest":
		var f string
		switch arg {
		case "wintun", "wintun-amd64":
			f = dir + "/wintun.dll"
		case "wintun-arm64":
			f = dir + "/wintun-arm64.dll"
		default:
			f = dir + "/bin/plug-" + binaryName(arg)
		}
		if !servable(arg, f) {
			fmt.Fprintf(stderr, "plug: no binary for %s on this agent\n", arg)
			return 1
		}
		sum, err := fileSHA256(f)
		if err != nil {
			fmt.Fprintf(stderr, "plug: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "sha256=%x\n", sum)
		if isRegular(f + ".sig") {
			sig, err := os.ReadFile(f + ".sig")
			if err != nil {
				fmt.Fprintf(stderr, "plug: %v\n", err)
				return 1
			}
			fmt.Fprintf(out, "sig=%s\n", strings.TrimRight(string(sig), "\n"))
		}
		return 0
	}

	// <os>-<arch>: that raw binary, for the launcher's core fetch and for
	// manual downloads.
	if f := dir + "/bin/plug-" + binaryName(verb); verb != "" && servable(verb, f) {
		return sendFile(out, stderr, f)
	}

	version, _ := os.ReadFile(dir + "/VERSION")
	fmt.Fprintf(stderr, "plug %s — usage over ssh:\n", strings.TrimRight(string(version), "\n"))
	fmt.Fprintln(stderr, "  # host key regenerated each start (not a secret) — skip the check:")
	fmt.Fprintln(stderr, "  ssh -p <port> -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@<host> install | sh   # install (linux/macOS, recommended)")
	fmt.Fprintln(stderr, "  ssh -n -p <port> -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@<host> install-windows | bash -s -- <host> <port>   # install (Windows, Git Bash)")
	return 1
}

// installVars is the lines an installer starts with: shebang, then what the
// command and whoever runs this agent decided for it. The profile to save is
// the name the command gave, else none and the installer names it after the
// host. A hosted image adds what to do with the key a hosted cluster requires,
// in the words of the gateway that embeds this agent. Every value is checked,
// because it is written into a script the developer runs. A refusal returns
// false and nothing is emitted, so nothing runs on the other side of the pipe.
func installVars(shebang, name, keyInstruction, dir string, stderr io.Writer) (string, bool) {
	if name != "" && !profileNameRe.MatchString(name) {
		fmt.Fprintf(stderr, "plug: '%s' is not a profile name: a letter or digit, then letters, digits, dot, dash or underscore (63 max)\n", name)
		return "", false
	}
	if !sayable(keyInstruction) {
		// Not said, and the installer falls back to its own sentence.
		keyInstruction = ""
	}
	hosted := ""
	if v, err := os.ReadFile(dir + "/VERSION"); err == nil && strings.HasSuffix(strings.TrimRight(string(v), "\n"), "-hosted") {
		hosted = "1"
	}
	vars := fmt.Sprintf("%s\n_name=%s\n_keyhint='%s'\n_hosted=%s\n", shebang, name, keyInstruction, hosted)
	// An agent that admits by approval has the installer make the first
	// contact, so the newcomer reads what to ask of whom before they leave the
	// install. Absent otherwise: an open agent's installer is what it was.
	if os.Getenv(admissionEnv) == "invite" {
		vars += "_admission=invite\n"
	}
	return vars, true
}

// sayable reports whether the gateway's sentence can go inside single quotes
// in the installer: bounded, no quote, no backslash, no control character (a
// line break among them).
func sayable(s string) bool {
	if len(s) > 200 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\'' || c == '\\' || c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// binaryName turns `<os>-<arch>` into the file it names under bin/: uname's
// spellings of the architecture are accepted, and Windows gets its .exe.
func binaryName(spec string) string {
	o, a, found := strings.Cut(spec, "-")
	if !found {
		a = spec
	}
	switch a {
	case "x86_64", "amd64":
		a = "amd64"
	case "aarch64", "arm64":
		a = "arm64"
	}
	ext := ""
	if o == "windows" {
		ext = ".exe"
	}
	return o + "-" + a + ext
}

// servable reports whether the file a request names is one to hand out. The
// path is built by concatenation and never cleaned, so `..` stays a component
// that has to exist; a slash in the request is refused outright all the same,
// since no file this account serves has one in its name.
func servable(request, path string) bool {
	return !strings.Contains(request, "/") && isRegular(path)
}

func isRegular(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func sendFile(out io.Writer, stderr io.Writer, path string) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(stderr, "plug: %v\n", err)
		return 1
	}
	defer f.Close()
	if _, err := io.Copy(out, f); err != nil {
		fmt.Fprintf(stderr, "plug: %v\n", err)
		return 1
	}
	return 0
}

// sendBase64 writes the file in base64, 76 columns to the line, as base64(1)
// does on Linux: 57 bytes make one full line.
func sendBase64(out io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 57*1024)
	line := make([]byte, 77)
	for {
		n, err := io.ReadFull(f, buf)
		for i := 0; i < n; i += 57 {
			k := base64.StdEncoding.EncodedLen(min(57, n-i))
			base64.StdEncoding.Encode(line, buf[i:min(i+57, n)])
			line[k] = '\n'
			if _, werr := out.Write(line[:k+1]); werr != nil {
				return werr
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func fileSHA256(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// asciiLower lowers A to Z and leaves every other byte alone, as tr 'A-Z'
// 'a-z' did: strings.ToLower would also fold letters outside ASCII.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
