package main

import (
	"strings"
	"testing"
)

// plug narrows its own $PATH when privileged, but the command you launch must
// keep yours — otherwise `plug -s … npm run dev` stops finding npm.
func TestWithUserPathRestoresTheHumansPath(t *testing.T) {
	saved := userPath
	userPath = "/home/me/.nvm/versions/node/v22/bin:/usr/local/bin"
	defer func() { userPath = saved }()

	got := withUserPath([]string{"HOME=/home/me", "PATH=/usr/sbin:/usr/bin", "TERM=xterm"})
	var paths []string
	for _, kv := range got {
		if strings.HasPrefix(kv, "PATH=") {
			paths = append(paths, kv)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("want exactly one PATH entry, got %v", paths)
	}
	if paths[0] != "PATH="+userPath {
		t.Errorf("child PATH = %q, want the human's %q", paths[0], userPath)
	}
	for _, want := range []string{"HOME=/home/me", "TERM=xterm"} {
		if !contains(got, want) {
			t.Errorf("withUserPath dropped %q", want)
		}
	}
	// nil env means "inherit", and must still carry the human's PATH
	if !contains(withUserPath(nil), "PATH="+userPath) {
		t.Error("withUserPath(nil) lost the human's PATH")
	}
}

// The launcher execs the core with this env, and the core captures $PATH at
// init as "the human's". Handing it the narrowed one silently breaks every
// command that lives outside the system directories — the whole launcher/core
// path, i.e. any session whose agent version differs from the launcher's.
func TestCoreEnvHandsTheCoreTheHumansPath(t *testing.T) {
	saved := userPath
	userPath = "/home/me/.nvm/versions/node/v22/bin:/usr/local/bin"
	defer func() { userPath = saved }()
	t.Setenv("PATH", "/usr/sbin:/usr/bin:/sbin:/bin") // as securePath left it

	var paths []string
	for _, kv := range coreEnv(config{host: "h", port: "2222"}) {
		if strings.HasPrefix(kv, "PATH=") {
			paths = append(paths, kv)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("want exactly one PATH entry, got %v", paths)
	}
	if paths[0] != "PATH="+userPath {
		t.Errorf("core PATH = %q, want the human's %q", paths[0], userPath)
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
