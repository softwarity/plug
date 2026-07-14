// plug-agent is the agent-side helper for the reverse direction (-s): it
// provisions the CLUSTER NAME a session serves, dynamically, with whatever the
// deployment gives it — and nothing more:
//
//   - Docker/Swarm, docker.sock mounted (opt-in in the stack file): create a
//     tiny SIGNPOST container carrying the DNS alias, relaying the port to the
//     agent. Names appear and disappear with the session — no stack redeploy.
//   - Kubernetes, RBAC applied (opt-in, deploy/plug-k8s-dynamic.yaml): create a
//     Service selecting the agent pod. Same lifecycle.
//   - Neither: answer "static" — the CLI falls back to pre-declared aliases.
//
// It is the ForceCommand of the `plug` SSH user, so it is ALSO the user's whole
// exec surface: the verbs below or nothing (no shell — a lockdown compared to
// the /bin/sh that user had before).
//
// Verbs (via SSH_ORIGINAL_COMMAND):
//
//	serve-name <name> <port>   provision name:port → this agent. One line out:
//	                           "dynamic" | "static" | "error: …"
//	unserve-name <name>        drop it ("ok" | "static" | "error: …")
//
// Direct argv modes (not reachable over SSH):
//
//	plug-agent signpost <port> <target>   the signpost container's process
//	plug-agent gc                         boot-time cleanup (entrypoint)
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "signpost":
			if len(args) != 3 {
				fatal("usage: plug-agent signpost <port> <target>")
			}
			signpost(args[1], args[2])
			return
		case "gc":
			gc()
			return
		}
	}
	// ForceCommand path: the client's command line arrives in SSH_ORIGINAL_COMMAND.
	dispatch(strings.Fields(os.Getenv("SSH_ORIGINAL_COMMAND")))
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

// answer prints the one-line protocol reply the CLI parses, and exits 0 — the
// reply itself carries success or failure ("error: …"), so the SSH exit status
// stays out of the contract (old CLIs never call us; old shells said 127).
func answer(format string, a ...any) {
	fmt.Printf(format+"\n", a...)
	os.Exit(0)
}

// A DNS label the way BOTH backends accept it: RFC 1035 (leading letter) so a
// k8s Service object is valid too — docker would take a leading digit, k8s
// won't, and -s must behave the same whichever backend answers.
var nameRe = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func dispatch(cmd []string) {
	if len(cmd) == 0 {
		fatal("plug-agent: this user runs the tunnel and the -s verbs; there is no shell")
	}
	switch cmd[0] {
	case "serve-name":
		if len(cmd) != 3 {
			answer("error: usage: serve-name <name> <port>")
		}
		name, port := cmd[1], cmd[2]
		if !nameRe.MatchString(name) {
			answer("error: %q is not a valid DNS label", name)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			answer("error: %q is not a valid port", port)
		}
		serveName(name, port)
	case "unserve-name":
		if len(cmd) != 2 || !nameRe.MatchString(cmd[1]) {
			answer("error: usage: unserve-name <name>")
		}
		unserveName(cmd[1])
	default:
		answer("error: unknown command %q", cmd[0])
	}
}

func serveName(name, port string) {
	if k8sAvailable() {
		k8sServe(name, port)
	}
	if dockerAvailable() {
		dockerServe(name, port)
	}
	answer("static")
}

func unserveName(name string) {
	if k8sAvailable() {
		k8sUnserve(name)
	}
	if dockerAvailable() {
		dockerUnserve(name)
	}
	answer("static")
}

func gc() {
	if k8sAvailable() {
		k8sGC()
	}
	if dockerAvailable() {
		dockerGC()
	}
}

// ---- signpost: the process inside the alias-bearing container ----

// signpost relays <:port> to <target> (the agent, by container name — resolved
// per-connection so it survives agent restarts). It is the whole job of the
// signpost container: carry the DNS alias, hand the bytes to the agent's sshd
// remote-forward listener.
func signpost(port, target string) {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		fatal("signpost: %v", err)
	}
	fmt.Printf("signpost: :%s -> %s\n", port, target)
	for {
		c, err := ln.Accept()
		if err != nil {
			fatal("signpost: %v", err)
		}
		go func() {
			t, err := net.DialTimeout("tcp", target, 5*time.Second)
			if err != nil {
				c.Close()
				return
			}
			relay(c, t)
		}()
	}
}

func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// ---- docker backend (sock mounted — the stack file's opt-in) ----

const dockerSock = "/var/run/docker.sock"

func dockerAvailable() bool {
	_, err := os.Stat(dockerSock)
	return err == nil
}

var dockerClient = &http.Client{
	Timeout: 20 * time.Second,
	Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", dockerSock, 5*time.Second)
		},
	},
}

func dockerAPI(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://docker"+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := dockerClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	if resp.StatusCode >= 300 {
		var e struct{ Message string `json:"message"` }
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("%s", firstNonEmpty(e.Message, strings.TrimSpace(string(data)), resp.Status))
	}
	return resp.StatusCode, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// containerID finds THIS container's real id from the cgroup/mountinfo the
// kernel exposes — authoritative, unlike the hostname (which a stack can
// override, and which would then GET a DIFFERENT container's inspect). Falls
// containerIDFromMount reads THIS container's id from the Docker-bind-mounted
// paths in mountinfo (`…/containers/<id>/{resolv.conf,hostname,hosts}`). The
// segment is anchored on `/containers/<64-hex>/`, NOT any 64-hex — the overlay
// layer hashes elsewhere in mountinfo are also 64-hex and must not be picked.
// "" when the pattern isn't present (returns to the hostname).
func containerIDFromMount() string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`/containers/([0-9a-f]{64})/`).FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// dnsNetworks are the docker networks with a working embedded DNS (so an alias
// resolves). The default `bridge`, `host` and `none` have none — an alias there
// is silently dead, so treat "only those" as no dynamic backend (→ static).
var undnsNetwork = map[string]bool{"bridge": true, "host": true, "none": true}

// netKind classifies one of the agent's networks. app is false for the Swarm
// `ingress` routing mesh (a signpost must never touch it). attachable reports
// whether a STANDALONE container can join it: a bridge can, a Swarm overlay only
// if created `attachable: true`. overlay reports a Swarm overlay — the ONLY kind
// a Swarm SERVICE can join (a service cannot attach a node-local bridge). On
// inspect error, assume a plain joinable bridge — the proven Compose path must
// not regress.
func netKind(name string) (app, attachable, overlay bool) {
	var n struct {
		Driver     string `json:"Driver"`
		Ingress    bool   `json:"Ingress"`
		Attachable bool   `json:"Attachable"`
	}
	if _, err := dockerAPI("GET", "/networks/"+name, nil, &n); err != nil {
		return true, true, false
	}
	if n.Ingress {
		return false, false, false
	}
	if n.Driver == "overlay" {
		return true, n.Attachable, true
	}
	return true, true, false // bridge and the like
}

type netRef struct {
	name       string
	attachable bool
	overlay    bool
}

type selfInfo struct {
	name    string   // agent container/task name — relay target for the container backend
	service string   // agent's Swarm service name (empty off Swarm) — relay target for the service backend
	image   string
	nets    []netRef // application networks (overlay/bridge), minus ingress/host/none
}

// attachableNets: networks a standalone signpost CONTAINER can join (bridge, or
// an attachable overlay).
func (s selfInfo) attachableNets() []string {
	var out []string
	for _, n := range s.nets {
		if n.attachable {
			out = append(out, n.name)
		}
	}
	return out
}

// overlayNets: the overlays a Swarm SERVICE signpost can join (attachable or
// not). Node-local bridges are excluded — a service cannot attach one.
func (s selfInfo) overlayNets() []string {
	var out []string
	for _, n := range s.nets {
		if n.overlay {
			out = append(out, n.name)
		}
	}
	return out
}

// owner is the label that ties a signpost to this agent for gc. In Swarm it's
// the STABLE service name (the task/container name churns on restart); off Swarm
// it's the container name.
func (s selfInfo) owner() string {
	if s.service != "" {
		return s.service
	}
	return s.name
}

// relayTarget is where a signpost sends the bytes: the agent's Swarm service
// (VIP → the agent task holding the session; assumes the usual single replica)
// or, off Swarm, the container name.
func (s selfInfo) relayTarget() string {
	if s.service != "" {
		return s.service
	}
	return s.name
}

// dockerSelf identifies OUR container. The hostname is the short container id by
// default and resolves directly — the proven path. Only if that GET fails (a
// stack that overrode the hostname) do we fall back to the authoritative id
// parsed from mountinfo.
func dockerSelf() (selfInfo, error) {
	var s selfInfo
	var insp struct {
		Name   string `json:"Name"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			Networks map[string]struct{} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	id, _ := os.Hostname()
	_, err := dockerAPI("GET", "/containers/"+id+"/json", nil, &insp)
	if err != nil {
		if mid := containerIDFromMount(); mid != "" && mid != id {
			_, err = dockerAPI("GET", "/containers/"+mid+"/json", nil, &insp)
		}
		if err != nil {
			return s, fmt.Errorf("cannot identify the agent container: %v", err)
		}
	}
	s.name = strings.TrimPrefix(insp.Name, "/")
	s.image = insp.Config.Image
	s.service = insp.Config.Labels["com.docker.swarm.service.name"]
	for n := range insp.NetworkSettings.Networks {
		if undnsNetwork[n] {
			continue
		}
		if app, att, ov := netKind(n); app {
			s.nets = append(s.nets, netRef{n, att, ov})
		}
	}
	return s, nil
}

// swarmManager reports whether this node can create Swarm services (a manager).
// When it can, the signpost is a SERVICE (joins non-attachable overlays too),
// not a standalone container.
func swarmManager() bool {
	var info struct {
		Swarm struct {
			ControlAvailable bool `json:"ControlAvailable"`
		} `json:"Swarm"`
	}
	if _, err := dockerAPI("GET", "/info", nil, &info); err != nil {
		return false
	}
	return info.Swarm.ControlAvailable
}

func signpostName(name string) string { return "plug-sp-" + name }

// nameTaken reports whether name already resolves to a NON-signpost container
// on one of the given networks — i.e. the real service is deployed. Serving on
// top of it would only add the signpost to DNS round-robin (silent
// interception), so both backends refuse it (k8s refuses via the 409 path).
func nameTaken(name string, nets []string) bool {
	mine := map[string]bool{}
	for _, n := range nets {
		mine[n] = true
	}
	var list []struct {
		Names           []string `json:"Names"`
		Labels          map[string]string `json:"Labels"`
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases []string `json:"Aliases"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if _, err := dockerAPI("GET", "/containers/json", nil, &list); err != nil {
		return false // can't tell — let Verify be the backstop
	}
	for _, c := range list {
		if c.Labels["plug.signpost"] == "1" {
			continue // our own signposts don't count
		}
		for _, nm := range c.Names {
			if strings.TrimPrefix(nm, "/") == name {
				return true
			}
		}
		for net, ep := range c.NetworkSettings.Networks {
			if !mine[net] {
				continue
			}
			for _, a := range ep.Aliases {
				if a == name {
					return true
				}
			}
		}
	}
	return false
}

// swarmNameTaken reports whether a NON-signpost Swarm service already owns name
// — by its service name (the cluster-wide resolvable name) or a network alias.
// GET /services lists the WHOLE cluster from a manager, so it also catches a
// real service whose tasks run on other nodes (which nameTaken's container scan
// cannot see). Serving on top would shadow it in DNS.
func swarmNameTaken(name string) bool {
	var list []struct {
		Spec struct {
			Name         string            `json:"Name"`
			Labels       map[string]string `json:"Labels"`
			TaskTemplate struct {
				Networks []struct {
					Aliases []string `json:"Aliases"`
				} `json:"Networks"`
			} `json:"TaskTemplate"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services", nil, &list); err != nil {
		return false // can't tell — Verify is the backstop
	}
	for _, s := range list {
		if s.Spec.Labels["plug.signpost"] == "1" {
			continue // our own signpost services don't count
		}
		if s.Spec.Name == name {
			return true
		}
		for _, n := range s.Spec.TaskTemplate.Networks {
			for _, a := range n.Aliases {
				if a == name {
					return true
				}
			}
		}
	}
	return false
}

// dockerServe picks the signpost shape. The agent runs as a Swarm SERVICE (it
// has a service name) AND this node can create services (a manager) → the
// signpost is a SERVICE, which joins the stack's overlay whether or not it is
// attachable. Otherwise (Compose, plain `docker run`, or a non-manager) it is a
// standalone CONTAINER, which needs a bridge or an attachable overlay.
func dockerServe(name, port string) {
	self, err := dockerSelf()
	if err != nil {
		answer("error: %v", err)
	}
	if self.service != "" && swarmManager() {
		swarmServe(name, port, self)
	}
	containerServe(name, port, self)
}

// containerServe runs the signpost as a standalone container — needs a network
// it can actually join (a bridge, or an attachable overlay).
func containerServe(name, port string, self selfInfo) {
	nets := self.attachableNets()
	if len(nets) == 0 {
		// Nothing a standalone container can join (only bridge/host, or a
		// non-attachable overlay off a Swarm manager). Fall back to static: a
		// pre-declared alias on the plug SERVICE still works, and Verify reports
		// precisely if it's absent.
		answer("static")
	}
	if nameTaken(name, nets) {
		answer("error: %q already resolves to a container in the cluster — the real service owns the name; stop it while you serve yours", name)
	}
	// Replace a leftover signpost for this name (a crashed session's, or a
	// re-run): rm -f is idempotent.
	_, _ = dockerAPI("DELETE", "/containers/"+signpostName(name)+"?force=1", nil, nil)

	endpoints := map[string]any{}
	for _, n := range nets {
		endpoints[n] = map[string]any{"Aliases": []string{name}}
	}
	body := map[string]any{
		"Image":      self.image,
		"Entrypoint": []string{"/usr/local/bin/plug-agent", "signpost", port, self.relayTarget() + ":" + port},
		"Labels": map[string]string{
			"plug.signpost":       "1",
			"plug.signpost.owner": self.owner(),
		},
		"HostConfig":       map[string]any{"NetworkMode": nets[0]},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{nets[0]: endpoints[nets[0]]}},
	}
	var created struct{ Id string `json:"Id"` }
	if _, err := dockerAPI("POST", "/containers/create?name="+signpostName(name), body, &created); err != nil {
		answer("error: creating the %s signpost: %v", name, err)
	}
	// The alias must exist on EVERY network the agent is on (workloads may look
	// from any of them).
	for _, n := range nets[1:] {
		if _, err := dockerAPI("POST", "/networks/"+n+"/connect",
			map[string]any{"Container": created.Id, "EndpointConfig": endpoints[n]}, nil); err != nil {
			_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
			answer("error: attaching the %s signpost to %s: %v", name, n, err)
		}
	}
	if _, err := dockerAPI("POST", "/containers/"+created.Id+"/start", nil, nil); err != nil {
		_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
		answer("error: starting the %s signpost: %v", name, err)
	}
	answer("dynamic")
}

// swarmServe runs the signpost as a Swarm SERVICE. A service joins the stack's
// overlay whether or not it is `attachable` — the whole reason this backend
// exists — and carries the alias there, relaying to the agent's service VIP.
func swarmServe(name, port string, self selfInfo) {
	// -s relays to the agent's service VIP, and the session's remote-forward
	// lives on ONE task — so >1 replica makes the VIP miss it intermittently.
	// Refuse loudly rather than ship a silent flaky path.
	if r := serviceReplicas(self.service); r > 1 {
		answer("error: the plug agent has %d replicas — plug -s needs a single replica (scale the plug service to 1)", r)
	}
	nets := self.overlayNets()
	if len(nets) == 0 {
		answer("static") // agent only on ingress/bridge — nothing to publish an alias on
	}
	// A real service with this name (anywhere in the cluster) must keep it: the
	// container-scan nameTaken can't see Swarm services, so check them explicitly.
	if swarmNameTaken(name) {
		answer("error: %q is a service in the cluster — the real service owns the name; stop it while you serve yours", name)
	}
	// Replace a leftover signpost service for this name (idempotent).
	_, _ = dockerAPI("DELETE", "/services/"+signpostName(name), nil, nil)

	var attach []map[string]any
	for _, n := range nets {
		attach = append(attach, map[string]any{"Target": n, "Aliases": []string{name}})
	}
	spec := map[string]any{
		"Name": signpostName(name),
		"Labels": map[string]string{
			"plug.signpost":       "1",
			"plug.signpost.owner": self.owner(),
		},
		"TaskTemplate": map[string]any{
			"ContainerSpec": map[string]any{
				"Image":   self.image,
				"Command": []string{"/usr/local/bin/plug-agent", "signpost", port, self.relayTarget() + ":" + port},
			},
			"Networks":      attach,
			"RestartPolicy": map[string]any{"Condition": "any"},
		},
		"Mode": map[string]any{"Replicated": map[string]any{"Replicas": 1}},
	}
	if _, err := dockerAPI("POST", "/services/create", spec, nil); err != nil {
		answer("error: creating the %s signpost service: %v", name, err)
	}
	answer("dynamic")
}

func dockerUnserve(name string) {
	// Drop whichever shape exists. The container shape is always meaningful; the
	// service shape only on a Swarm manager (off one, /services/* answers 503 —
	// not a real failure, so don't try it there). A real non-404 failure on
	// either shape is surfaced (a swallowed error would leak the signpost).
	cc, ec := dockerAPI("DELETE", "/containers/"+signpostName(name)+"?force=1", nil, nil)
	if ec != nil && cc != 404 {
		answer("error: removing the %s signpost: %v", name, ec)
	}
	if swarmManager() {
		if sc, es := dockerAPI("DELETE", "/services/"+signpostName(name), nil, nil); es != nil && sc != 404 {
			answer("error: removing the %s signpost service: %v", name, es)
		}
	}
	answer("ok")
}

// dockerGC sweeps, at agent boot, THIS agent's own orphaned signposts (an agent
// restart leaves its sessions' signposts running). A signpost is ours if its
// owner label is our current name OR its owner container no longer exists — the
// latter covers Swarm, where the agent's container name churns on restart, so
// the old signposts' owner never equals the new name but their owner container
// is gone. This leaves a CO-LOCATED other agent's live signposts (owner still
// running) untouched — the pure-shared-network scan used to wipe those.
func dockerGC() {
	self, err := dockerSelf()
	if err != nil {
		return
	}
	mine := self.owner()
	swarm := swarmManager()
	f := `{"label":["plug.signpost=1"]}`
	// Standalone-container signposts.
	var clist []struct {
		Id     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if _, err := dockerAPI("GET", "/containers/json?all=1&filters="+urlEscape(f), nil, &clist); err == nil {
		for _, c := range clist {
			o := c.Labels["plug.signpost.owner"]
			if o == mine || !ownerAlive(o, swarm) {
				_, _ = dockerAPI("DELETE", "/containers/"+c.Id+"?force=1", nil, nil)
			}
		}
	}
	// Swarm-service signposts (only reachable on a manager).
	if !swarm {
		return
	}
	var slist []struct {
		ID   string `json:"ID"`
		Spec struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services?filters="+urlEscape(f), nil, &slist); err == nil {
		for _, s := range slist {
			o := s.Spec.Labels["plug.signpost.owner"]
			if o == mine || !ownerAlive(o, swarm) {
				_, _ = dockerAPI("DELETE", "/services/"+s.ID, nil, nil)
			}
		}
	}
}

// ownerAlive reports whether the owner agent still exists. Off a Swarm manager
// (swarm=false) only containers can be owners AND /services/* answers 503, so we
// must NOT consult serviceExists there — otherwise its non-404 makes ownerAlive
// unconditionally true and the orphan sweep never fires (the Compose regression
// the review caught). An empty owner counts as gone.
func ownerAlive(name string, swarm bool) bool {
	if name == "" {
		return false
	}
	if containerExists(name) {
		return true
	}
	return swarm && serviceExists(name)
}

// containerExists reports whether a container named `name` is present (running
// or not).
func containerExists(name string) bool {
	code, err := dockerAPI("GET", "/containers/"+name+"/json", nil, nil)
	return err == nil || code != 404
}

// serviceExists reports whether a Swarm service named `name` is present.
func serviceExists(name string) bool {
	code, err := dockerAPI("GET", "/services/"+name, nil, nil)
	return err == nil || code != 404
}

// serviceReplicas returns the replica count of a Swarm service (1 if it can't
// tell, or for a global/unset mode — don't block on uncertainty).
func serviceReplicas(name string) int {
	var s struct {
		Spec struct {
			Mode struct {
				Replicated struct {
					Replicas int `json:"Replicas"`
				} `json:"Replicated"`
			} `json:"Mode"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+name, nil, &s); err != nil {
		return 1
	}
	if s.Spec.Mode.Replicated.Replicas == 0 {
		return 1
	}
	return s.Spec.Mode.Replicated.Replicas
}

func urlEscape(s string) string {
	return strings.NewReplacer("{", "%7B", "}", "%7D", `"`, "%22", "[", "%5B", "]", "%5D", ",", "%2C", ":", "%3A", "=", "%3D").Replace(s)
}

// ---- kubernetes backend (RBAC applied — deploy/plug-k8s-dynamic.yaml) ----

const k8sSA = "/var/run/secrets/kubernetes.io/serviceaccount"

func k8sAvailable() bool {
	_, err := os.Stat(k8sSA + "/token")
	return err == nil
}

func k8sAPI(method, path string, body any, out any) (int, error) {
	token, err := os.ReadFile(k8sSA + "/token")
	if err != nil {
		return 0, err
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(k8sSA + "/ca.crt"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	client := &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "https://kubernetes.default.svc"+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		_ = json.Unmarshal(data, out)
	}
	if resp.StatusCode >= 300 {
		var e struct{ Message string `json:"message"` }
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, fmt.Errorf("%s", firstNonEmpty(e.Message, resp.Status))
	}
	return resp.StatusCode, nil
}

func k8sNamespace() string {
	ns, _ := os.ReadFile(k8sSA + "/namespace")
	return strings.TrimSpace(string(ns))
}

const k8sManaged = "app.kubernetes.io/managed-by"

func k8sServe(name, port string) {
	ns := k8sNamespace()
	p, _ := strconv.Atoi(port)
	svc := map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": name, "labels": map[string]string{k8sManaged: "plug"}},
		"spec": map[string]any{
			// The official manifest labels the agent `app: plug` — the Service
			// (the k8s "signpost") points the name at it.
			"selector": map[string]string{"app": "plug"},
			"ports":    []map[string]any{{"port": p, "targetPort": p}},
		},
	}
	code, err := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil)
	switch {
	case err == nil:
		answer("dynamic")
	case code == 403:
		// No RBAC → the opt-in isn't applied. Not an error: static mode.
		answer("static")
	case code == 409:
		// The name exists. Take it over ONLY if a previous plug session made it
		// (a crashed session's leftover); a real service keeps its name.
		var existing struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}
		_, gerr := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing)
		if gerr != nil || existing.Metadata.Labels[k8sManaged] != "plug" {
			answer("error: the Service %q already exists and is not plug's — the real service owns the name", name)
		}
		// It's ours: replace it. If the re-create fails, say SO — do not fall
		// through to the "not plug's" lie (the name is now deleted; report the
		// real cause so the session aborts with an accurate remedy).
		_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil)
		if _, rerr := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil); rerr != nil {
			answer("error: re-provisioning the Service %q failed (a stale plug Service was removed): %v", name, rerr)
		}
		answer("dynamic")
	default:
		answer("error: %v", err)
	}
}

func k8sUnserve(name string) {
	ns := k8sNamespace()
	var existing struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if _, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing); err != nil ||
		existing.Metadata.Labels[k8sManaged] != "plug" {
		answer("static") // not ours (or RBAC absent) — nothing to drop
	}
	if _, err := k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil); err != nil {
		answer("error: %v", err)
	}
	answer("ok")
}

func k8sGC() {
	ns := k8sNamespace()
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if _, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services?labelSelector="+k8sManaged+"%3Dplug", nil, &list); err != nil {
		return
	}
	for _, s := range list.Items {
		_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+s.Metadata.Name, nil, nil)
	}
}
