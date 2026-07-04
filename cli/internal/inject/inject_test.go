package inject

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestEnvDisabled: PLUG_NO_INJECT makes Env a no-op.
func TestEnvDisabled(t *testing.T) {
	t.Setenv(EnvVarDisable, "1")
	if env := Env("127.0.0.1:1080", nil); env != nil {
		t.Fatalf("Env should be nil when %s=1, got %v", EnvVarDisable, env)
	}
}

// TestEnvShape: when available, Env returns exactly the loader var and PLUG_SOCKS,
// the extracted lib exists and is executable, and the SOCKS address is passed through.
func TestEnvShape(t *testing.T) {
	t.Setenv(EnvVarDisable, "") // ensure enabled
	if !Available() {
		t.Skipf("no embedded hook for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	// Redirect HOME so we don't touch the user's ~/.plug.
	t.Setenv("HOME", t.TempDir())

	env := Env("127.0.0.1:12345", nil)
	if env == nil {
		t.Fatal("Env returned nil though Available() is true")
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	loader := "LD_PRELOAD"
	if runtime.GOOS == "darwin" {
		loader = "DYLD_INSERT_LIBRARIES"
	}
	lib := got[loader]
	if lib == "" {
		t.Fatalf("missing %s in %v", loader, env)
	}
	if got[EnvVarSocks] != "127.0.0.1:12345" {
		t.Fatalf("%s = %q, want 127.0.0.1:12345", EnvVarSocks, got[EnvVarSocks])
	}
	// The lib path (last ':'-separated entry) must exist and be executable.
	parts := strings.Split(lib, ":")
	p := parts[len(parts)-1]
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("extracted lib %q: %v", p, err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("extracted lib %q not executable (mode %v)", p, fi.Mode())
	}
	if filepath.Dir(p) != filepath.Join(os.Getenv("HOME"), ".plug", "lib") {
		t.Fatalf("lib written to %q, want ~/.plug/lib", filepath.Dir(p))
	}
}

// TestAppendPreload keeps any pre-existing loader list and appends ours.
func TestAppendPreload(t *testing.T) {
	if got := appendPreload("", "/x/lib.so"); got != "/x/lib.so" {
		t.Fatalf("empty: got %q", got)
	}
	if got := appendPreload("/a/b.so", "/x/lib.so"); got != "/a/b.so:/x/lib.so" {
		t.Fatalf("append: got %q", got)
	}
}

// TestInjectionEndToEnd is the real proof: a local SOCKS5 proxy that always dials
// a local HTTP backend (so any name it is asked to CONNECT resolves proxy-side),
// and a real libc child (a tiny C program) launched with inject.Env connecting to
// a MADE-UP name that does not resolve on this host. If it reaches the backend,
// the hook intercepted connect()+getaddrinfo() and routed through SOCKS with
// remote resolution — exactly plug's runtime behavior.
func TestInjectionEndToEnd(t *testing.T) {
	if !Available() {
		t.Skipf("no embedded hook for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	cc := findCC(t)

	// Backend: HTTP server that echoes Host+path.
	backend := startBackend(t)

	// SOCKS5 proxy that always dials the backend, recording the requested target.
	socksAddr, lastTarget := startSOCKS(t, backend)

	// Compile the tiny libc client.
	client := buildCClient(t, cc)

	// A name that does not resolve locally.
	const name = "made-up-cluster-svc"
	port := portOf(backend)

	env := Env(socksAddr, nil)
	if env == nil {
		t.Fatal("Env nil despite Available()")
	}
	// Extend the current environment (the child needs a real PATH etc.).
	full := append(os.Environ(), env...)
	full = append(full, "PLUG_HOOK_DEBUG=1")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client, name, port)
	cmd.Env = full
	out, err := cmd.CombinedOutput()
	t.Logf("child output:\n%s", out)
	if err != nil {
		t.Fatalf("child failed: %v", err)
	}
	if !strings.Contains(string(out), "BACKEND-REACHED") {
		t.Fatalf("child did not reach backend via SOCKS; output:\n%s", out)
	}
	if got := lastTarget(); !strings.HasPrefix(got, name+":") {
		t.Fatalf("proxy was asked to reach %q, want the made-up name %q (remote DNS)", got, name)
	}
}

// ---- helpers ----

func findCC(t *testing.T) string {
	t.Helper()
	for _, c := range []string{"cc", "clang", "gcc"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	t.Skip("no C compiler (cc/clang/gcc) available to build the test client")
	return ""
}

func startBackend(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "BACKEND-REACHED host=%s path=%s", r.Host, r.URL.Path)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln
}

func portOf(ln net.Listener) string {
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	return p
}

// startSOCKS runs a minimal SOCKS5 (no-auth, CONNECT) proxy that always dials
// backend, and returns its address plus an accessor for the last requested
// target host:port.
func startSOCKS(t *testing.T, backend net.Listener) (string, func() string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	last := ""
	get := func() string { mu.Lock(); defer mu.Unlock(); return last }

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				target, err := socksHandshakeTest(c)
				if err != nil {
					return
				}
				mu.Lock()
				last = target
				mu.Unlock()
				remote, err := net.Dial("tcp", backend.Addr().String())
				if err != nil {
					c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
					return
				}
				defer remote.Close()
				c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
				go io.Copy(remote, c)
				io.Copy(c, remote)
			}()
		}
	}()
	return ln.Addr().String(), get
}

func socksHandshakeTest(c net.Conn) (string, error) {
	buf := make([]byte, 262)
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return "", err
	}
	if buf[0] != 0x05 {
		return "", fmt.Errorf("not socks5")
	}
	n := int(buf[1])
	if _, err := io.ReadFull(c, buf[:n]); err != nil {
		return "", err
	}
	c.Write([]byte{0x05, 0x00})
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return "", err
	}
	if buf[1] != 0x01 {
		return "", fmt.Errorf("not CONNECT")
	}
	var host string
	switch buf[3] {
	case 0x01:
		io.ReadFull(c, buf[:4])
		host = net.IP(buf[:4]).String()
	case 0x03:
		io.ReadFull(c, buf[:1])
		l := int(buf[0])
		io.ReadFull(c, buf[:l])
		host = string(buf[:l])
	case 0x04:
		io.ReadFull(c, buf[:16])
		host = net.IP(buf[:16]).String()
	default:
		return "", fmt.Errorf("bad atyp")
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return "", err
	}
	port := binary.BigEndian.Uint16(buf[:2])
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

// buildCClient compiles a tiny libc HTTP client to a temp binary and returns its
// path. It resolves argv[1]:argv[2], connects, GETs /probe, prints the response.
func buildCClient(t *testing.T, cc string) string {
	t.Helper()
	src := `
#include <netdb.h>
#include <stdio.h>
#include <string.h>
#include <signal.h>
#include <unistd.h>
#include <stdlib.h>
#include <sys/socket.h>
static void ka(int s){(void)s;const char*m="ALARM\n";write(2,m,6);_exit(42);}
int main(int c,char**v){
  signal(SIGALRM,ka);alarm(6);
  const char*h=c>1?v[1]:"x"; const char*p=c>2?v[2]:"80";
  struct addrinfo hints,*res=0; memset(&hints,0,sizeof hints);
  hints.ai_family=AF_INET; hints.ai_socktype=SOCK_STREAM;
  int rc=getaddrinfo(h,p,&hints,&res);
  if(rc){fprintf(stderr,"getaddrinfo:%s\n",gai_strerror(rc));return 3;}
  int fd=socket(AF_INET,SOCK_STREAM,0);
  if(connect(fd,res->ai_addr,res->ai_addrlen)){perror("connect");return 4;}
  char req[256]; snprintf(req,sizeof req,"GET /probe HTTP/1.0\r\nHost: %s\r\n\r\n",h);
  write(fd,req,strlen(req));
  char buf[512]; ssize_t n=read(fd,buf,sizeof buf-1);
  if(n>0){buf[n]=0;fputs(buf,stdout);} else {fprintf(stderr,"no data\n");return 5;}
  freeaddrinfo(res); return 0;
}
`
	dir := t.TempDir()
	csrc := filepath.Join(dir, "c.c")
	if err := os.WriteFile(csrc, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "cclient")
	cmd := exec.Command(cc, "-O1", "-o", bin, csrc)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compiling test client: %v\n%s", err, out)
	}
	return bin
}
