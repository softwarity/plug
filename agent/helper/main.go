// plug-agent is the agent-side helper for the reverse direction (-s): it
// provisions the CLUSTER NAME a session serves, dynamically, with whatever the
// deployment gives it — and nothing more:
//
//   - Docker/Swarm, docker.sock mounted (opt-in in the stack file): create a
//     tiny SIGNPOST container carrying the DNS alias, relaying the port to the
//     agent. Names appear and disappear with the session — no stack redeploy.
//   - Kubernetes (RBAC granted by deploy/plug-k8s.yaml): create a
//     Service selecting the agent pod. Same lifecycle.
//   - Neither: answer an error naming the access it is missing. There is no
//     fallback mode: a name you must pre-declare cluster-side is the very
//     coordination -s exists to remove.
//
// It is the ForceCommand of the `plug` SSH user, so it is ALSO the user's whole
// exec surface: the verbs below or nothing (no shell — a lockdown compared to
// the /bin/sh that user had before).
//
// Verbs (via SSH_ORIGINAL_COMMAND):
//
//	serve-name <name> <port> takeover
//	                           provision name:port → this agent. One line out:
//	                           "dynamic" | "dynamic parked" | "error: …"
//	                           A REAL workload owning the name is
//	                           parked for the session (containers stopped, Swarm
//	                           service scaled to 0, k8s Service repointed) — the
//	                           parking receipt rides the signpost's labels (or a
//	                           k8s annotation), and unserve-name/gc restore it.
//	unserve-name <name>        drop it, restoring anything parked
//	                           ("ok" | "error: …")
//
// Direct argv modes (not reachable over SSH):
//
//	preflight                  refuse to boot without orchestrator access
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
	"net/url"
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
		case "preflight":
			preflight()
			return
		}
	}
	// ForceCommand path: the client's command line arrives in SSH_ORIGINAL_COMMAND.
	dispatch(strings.Fields(os.Getenv("SSH_ORIGINAL_COMMAND")))
}

// preflight refuses to start an agent that cannot do the job it is deployed
// for. plug exists to plug services into a cluster: an agent with no way to
// create a name can carry sessions but not serve one, and a deployment that
// only reveals that the first time someone runs -s has hidden a missing mount
// or a missing RBAC behind an otherwise healthy container. Fail here, once, at
// the moment the stack file is in front of whoever wrote it.
func preflight() {
	if k8sAvailable() || dockerAvailable() {
		return
	}
	fatal("plug-agent: no orchestrator access, so this agent cannot create cluster names.\n" +
		"  Docker / Compose / Swarm: mount /var/run/docker.sock into the agent\n" +
		"      volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]\n" +
		"      (on Swarm, also run it as a service on a MANAGER node)\n" +
		"  Kubernetes: apply the RBAC that lets it manage Services\n" +
		"      kubectl apply -f deploy/plug-k8s.yaml\n" +
		"  Full stack files: https://softwarity.github.io/plug/")
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
		if len(cmd) != 4 || cmd[3] != "takeover" {
			answer("error: usage: serve-name <name> <port> takeover")
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
	case "info":
		// One parsable line for `plug doctor`: the agent's version and which
		// dynamic -s backend THIS deployment actually has — the answer to "will
		// -s be dynamic here, and is the image current?" asked from the outside.
		ver := localVersion()
		backend := "none"
		switch {
		case k8sAvailable():
			backend = "kubernetes"
		case dockerAvailable():
			backend = "docker"
			var inf struct {
				Swarm struct {
					LocalNodeState string `json:"LocalNodeState"`
				} `json:"Swarm"`
			}
			if code, err := dockerAPI("GET", "/info", nil, &inf); err == nil && code == 200 &&
				inf.Swarm.LocalNodeState == "active" {
				backend = "docker-swarm"
			}
		}
		answer("version=%s backend=%s", ver, backend)
	case "resolve":
		// Does <name> exist in THIS cluster? The CLI asks before minting a fake
		// IP for a bare name, so an absent name gets an honest NXDOMAIN instead
		// of a fake that can only refuse the connect (the Docker-Desktop
		// DNS-leak fix). Resolution runs here, through the cluster's own
		// resolver — the only place that truth lives. Both outcomes answer on
		// stdout ("found"/"nxdomain"): an error would be indistinguishable from
		// a pre-2.2 agent's "unknown command", which means "mint as before".
		if len(cmd) != 2 || !nameRe.MatchString(cmd[1]) {
			answer("error: usage: resolve <name>")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		addrs, err := net.DefaultResolver.LookupHost(ctx, cmd[1])
		cancel()
		for _, a := range addrs {
			// 198.18.0.0/15 is the range plug itself mints fakes from — an
			// answer there can only be an ECHO of a plug resolver upstream
			// (cluster on a plugged workstation: embedded DNS → VM → host DNS
			// → plug), never a real cluster service. Filtering it here is what
			// makes the whole check immune to that loop.
			if ip := net.ParseIP(a).To4(); ip != nil && ip[0] == 198 && ip[1]&0xFE == 18 {
				continue
			}
			if err == nil {
				answer("found")
			}
		}
		answer("nxdomain")
	case "self-update":
		// An optional target names WHERE to go — a stream (latest, a branch) or
		// the word `tag` for the newest release. Without it, follow the tag the
		// deployment already carries.
		// `plug update` — move THIS agent to the newest release, each backend its
		// own way. A deployment pinned to a release tag has that tag REWRITTEN
		// (2.3.0 → 2.4.0): plug is infrastructure, not an application dependency
		// to hold back, and re-resolving a pin can only ever return the same
		// image. A moving tag (latest, main, a branch) belongs to its publisher
		// and is only re-pulled.
		//
		// One line out; the FIRST WORD is the verdict the CLI parses:
		//   updating …   a redeploy was triggered (k8s rolling / swarm update)
		//   current …    already the newest release, or a moving tag that did
		//                not move — answered WITHOUT rolling anything, so the
		//                CLI does not poll for a change that cannot come
		//   pulled …     newer image pulled; recreating is the caller's move
		//   error: …     no orchestrator access, RBAC gap, not a manager, …
		want := ""
		if len(cmd) == 2 {
			want = cmd[1]
		} else if len(cmd) > 2 {
			answer("error: usage: self-update [<tag>|tag|latest]")
		}
		selfUpdate(want)
	default:
		answer("error: unknown command %q", cmd[0])
	}
}

// localVersion is this agent's own version, baked into the image.
func localVersion() string {
	if b, err := os.ReadFile("/opt/plug/VERSION"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "unknown"
}

func serveName(name, port string) {
	if k8sAvailable() {
		k8sServe(name, port)
	}
	if dockerAvailable() {
		dockerServe(name, port)
	}
	// No orchestrator access: this agent cannot create the name. There is no
	// half-mode to fall back to — pre-declaring an alias per name is the exact
	// coordination -s removes — so say what is missing and let the session fail.
	answer("error: this agent has no orchestrator access, so it cannot create cluster names. " +
		"Mount /var/run/docker.sock on it (Compose/Swarm), or apply the Kubernetes RBAC (deploy/plug-k8s.yaml)")
}

func unserveName(name string) {
	if k8sAvailable() {
		k8sUnserve(name)
	}
	if dockerAvailable() {
		dockerUnserve(name)
	}
	answer("ok") // nothing provisioned here, nothing to drop
}

func gc() {
	if k8sAvailable() {
		k8sGC()
	}
	if dockerAvailable() {
		dockerGC()
	}
}

// ---- self-update: refresh THIS agent from its registry, per backend ----

func selfUpdate(want string) {
	if k8sAvailable() {
		k8sSelfUpdate(want)
	}
	if dockerAvailable() {
		self, err := dockerSelf()
		if err != nil {
			answer("error: %v", err)
		}
		if self.service != "" {
			swarmSelfUpdate(self, want)
		}
		dockerPlainSelfUpdate(self, want)
	}
	answer("error: this agent has no orchestrator access, so it cannot update itself — redeploy it by hand")
}

// k8sSelfUpdate updates the agent's own Deployment. A pinned RELEASE tag is
// rewritten to the newest release — a rolling restart alone would re-pull the
// same pin forever. A moving tag keeps the restart-only path (the annotation
// patch `kubectl rollout restart` uses), which makes the node re-pull it per
// its imagePullPolicy — Always in the official manifest.
func k8sSelfUpdate(want string) {
	ns := k8sNamespace()
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name  string `json:"name"`
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	code, err := k8sAPI("GET", "/apis/apps/v1/namespaces/"+ns+"/deployments?labelSelector=app%3Dplug", nil, &list)
	if err != nil {
		if code == 403 {
			answer("error: the deployed RBAC predates self-update — re-apply deploy/plug-k8s.yaml (it adds the deployments grant), or run: kubectl -n %s rollout restart deployment plug", ns)
		}
		answer("error: finding the agent deployment: %v", err)
	}
	if len(list.Items) == 0 {
		answer("error: no deployment labeled app=plug in namespace %s — restart the agent's workload by hand", ns)
	}
	dep := list.Items[0]
	name := dep.Metadata.Name

	// Find the container running the agent image — a pod may carry sidecars,
	// and patching the wrong one would be silent.
	img, container := "", ""
	for _, c := range dep.Spec.Template.Spec.Containers {
		if strings.Contains(c.Image, "plug") {
			img, container = c.Image, c.Name
			break
		}
	}
	if img == "" && len(dep.Spec.Template.Spec.Containers) == 1 {
		img, container = dep.Spec.Template.Spec.Containers[0].Image, dep.Spec.Template.Spec.Containers[0].Name
	}

	target, plan, note := updatePlan(img, want)
	if plan == planCurrent {
		answer("current %s", note)
	}
	// The restart annotation goes on either way: with a new image it makes the
	// rollout unambiguous, with a moving tag it IS the update.
	template := map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{"plug.softwarity.io/restartedAt": time.Now().UTC().Format(time.RFC3339)},
	}}
	if plan == planRetarget && container != "" {
		template["spec"] = map[string]any{"containers": []map[string]any{
			{"name": container, "image": target},
		}}
	}
	patch := map[string]any{"spec": map[string]any{"template": template}}
	if code, err := k8sMergePatch("/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, patch); err != nil {
		if code == 403 {
			answer("error: the deployed RBAC predates self-update — re-apply deploy/plug-k8s.yaml (it adds the deployments grant), or run: kubectl -n %s rollout restart deployment %s", ns, name)
		}
		answer("error: updating deployment %s: %v", name, err)
	}
	answer("updating deployment %s (namespace %s) — %s", name, ns, note)
}

// swarmSelfUpdate rolls the agent's own service. A pinned RELEASE tag is moved
// to the newest release (that is the whole point — re-resolving a pin can only
// ever return the same image); a moving tag is left as it is and merely
// re-resolved. Either way the pinned digest is dropped from the image (stack
// deploy pins one — with it, no update ever changes anything) and ForceUpdate
// rolls the task even when the digest comes back unchanged.
func swarmSelfUpdate(self selfInfo, want string) {
	if !swarmManager() {
		answer("error: the agent's node is not a swarm manager — from one, run: docker service update --image %s %s",
			retargetImageOnly(self.image), self.service)
	}
	var s struct {
		ID      string `json:"ID"`
		Version struct {
			Index int `json:"Index"`
		} `json:"Version"`
		Spec map[string]any `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+self.service, nil, &s); err != nil {
		answer("error: reading service %s: %v", self.service, err)
	}
	tt, _ := s.Spec["TaskTemplate"].(map[string]any)
	if tt == nil {
		answer("error: service %s has no task template", self.service)
	}
	img := self.image
	if cs, _ := tt["ContainerSpec"].(map[string]any); cs != nil {
		if is, _ := cs["Image"].(string); is != "" {
			img = is
		}
		if i := strings.Index(img, "@sha256:"); i > 0 {
			img = img[:i]
		}
	}
	target, plan, note := updatePlan(img, want)
	// Already the newest release: say so NOW rather than roll the task and let
	// the CLI poll 90s for a version that cannot change. The tag is a pin and
	// the registry has nothing above it — there is nothing to re-resolve.
	if plan == planCurrent {
		answer("current %s", note)
	}
	if cs, _ := tt["ContainerSpec"].(map[string]any); cs != nil {
		cs["Image"] = target
	}
	fu, _ := tt["ForceUpdate"].(float64)
	tt["ForceUpdate"] = int(fu) + 1
	if _, err := dockerAPI("POST", "/services/"+s.ID+"/update?version="+strconv.Itoa(s.Version.Index), s.Spec, nil); err != nil {
		answer("error: updating service %s: %v", self.service, err)
	}
	answer("updating service %s — %s, and the task rolls", self.service, note)
}

// dockerPlainSelfUpdate (Compose / plain `docker run`): pull the deployed tag
// and compare image ids. A container cannot recreate ITSELF, so when something
// newer landed the answer carries the one command the caller runs — with the
// image already local, that recreate is instant.
func dockerPlainSelfUpdate(self selfInfo, want string) {
	ver := localVersion()
	img := self.image
	if strings.HasPrefix(img, "sha256:") {
		answer("error: the agent was started from an image ID, not a tag — recreate it from a tag (softwarity/plug:latest) so updates can pull")
	}
	if i := strings.Index(img, "@sha256:"); i > 0 {
		img = img[:i]
	}
	target, plan, note := updatePlan(img, want)
	if plan == planCurrent {
		answer("current %s", note)
	}
	// Pull the image the deployment should end up on — the new tag when the pin
	// moves, the same one when it is a moving tag. Either way it is local by the
	// time the operator runs the recreate, so that step is instant.
	if err := dockerPull(target); err != nil {
		answer("current v%s — could not pull %s (%v)", ver, target, err)
	}
	// A retarget always warrants the recreate: the container runs an image the
	// deployment no longer names. Only a moving tag can legitimately turn out
	// to be unchanged.
	if plan == planResolve {
		var pulled struct {
			Id string `json:"Id"`
		}
		if _, err := dockerAPI("GET", "/images/"+target+"/json", nil, &pulled); err != nil {
			answer("current v%s — could not inspect %s after the pull (%v)", ver, target, err)
		}
		if pulled.Id == self.imageID {
			answer("current v%s — image %s unchanged", ver, target)
		}
	}
	// A container cannot recreate itself. Hand back the exact command — and
	// when the tag moved, the compose file has to be edited first, or the next
	// `up` would put the old pin straight back.
	how := "recreate the agent container with " + target
	if self.compose != "" {
		how = "docker compose up -d " + self.compose
		if plan == planRetarget {
			how = "set the plug service's image to " + target + " in your compose file, then: " + how
		}
	} else if plan == planRetarget {
		how = "recreate the agent container from " + target
	}
	answer("pulled %s — %s; the agent cannot recreate its own container: %s", target, note, how)
}

// dockerPull pulls ref (name[:tag]) through the daemon, draining the progress
// stream — the API answers 200 and reports failures IN the stream. Its own
// client: the pull outlives the 20s the control-plane calls are bounded to.
func dockerPull(ref string) error {
	name, tag := ref, "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name, tag = ref[:i], ref[i+1:]
	}
	cl := &http.Client{Timeout: 3 * time.Minute, Transport: dockerClient.Transport}
	resp, err := cl.Post("http://docker/images/create?fromImage="+url.QueryEscape(name)+"&tag="+url.QueryEscape(tag), "text/plain", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("%s", firstNonEmpty(e.Message, strings.TrimSpace(string(data)), resp.Status))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("%s", m.Error)
		}
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
		var e struct {
			Message string `json:"message"`
		}
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
// is silently dead, so "only those" means the agent cannot carry a name at all.
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
	name    string // agent container/task name — relay target for the container backend
	service string // agent's Swarm service name (empty off Swarm) — relay target for the service backend
	image   string
	imageID string   // resolved image id — what self-update compares a fresh pull against
	compose string   // compose service name (empty outside Compose) — the recreate hint
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
		Image  string `json:"Image"` // the resolved image ID (sha256:…)
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
	s.imageID = insp.Image
	s.compose = insp.Config.Labels["com.docker.compose.service"]
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

// Parking receipts — how a takeover is undone. The signpost created for the
// session carries, in its labels, exactly what was parked; unserve-name and the
// boot gc read it back and restore. Labels are immutable, so the receipt is
// written at signpost creation, before anything is parked.
const (
	parkedContainersLabel = "plug.parked.containers" // comma-joined container ids to restart
	parkedServiceLabel    = "plug.parked.service"    // Swarm service to scale back
	parkedReplicasLabel   = "plug.parked.replicas"   // …to this replica count
)

// owner is one RUNNING non-signpost container that answers to a name.
type owner struct {
	id   string
	name string // primary container name, for messages
}

// nameOwners returns the RUNNING NON-signpost containers name already resolves
// to on one of the given networks — i.e. the real service is deployed. Serving
// on top of one would only add the signpost to DNS round-robin (silent
// interception), so the caller either refuses or — takeover — parks them.
func nameOwners(name string, nets []string) []owner {
	mine := map[string]bool{}
	for _, n := range nets {
		mine[n] = true
	}
	var list []struct {
		Id              string            `json:"Id"`
		Names           []string          `json:"Names"`
		Labels          map[string]string `json:"Labels"`
		NetworkSettings struct {
			Networks map[string]struct{} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if _, err := dockerAPI("GET", "/containers/json", nil, &list); err != nil {
		return nil // can't tell — let Verify be the backstop
	}
	var owners []owner
	for _, c := range list {
		if c.Labels["plug.signpost"] == "1" {
			continue // our own signposts don't count
		}
		primary := c.Id[:12]
		if len(c.Names) > 0 {
			primary = strings.TrimPrefix(c.Names[0], "/")
		}
		named := false
		for _, nm := range c.Names {
			if strings.TrimPrefix(nm, "/") == name {
				named = true
				break
			}
		}
		// The network alias is how a Compose service is reached by name — but
		// /containers/json returns its aliases as null (Docker only fills them
		// in on inspect), so a real service reached by its service-name alias
		// would slip through here. Inspect the candidates that share a network
		// with us to read the aliases reliably.
		if !named {
			onMine := false
			for net := range c.NetworkSettings.Networks {
				if mine[net] {
					onMine = true
					break
				}
			}
			named = onMine && containerHasAlias(c.Id, name, mine)
		}
		if named {
			owners = append(owners, owner{id: c.Id, name: primary})
		}
	}
	return owners
}

// containerHasAlias reports whether the container answers to name on one of our
// networks — read from inspect, where the aliases (and DNSNames) are populated,
// unlike the container list.
func containerHasAlias(id, name string, mine map[string]bool) bool {
	var insp struct {
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases  []string `json:"Aliases"`
				DNSNames []string `json:"DNSNames"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if _, err := dockerAPI("GET", "/containers/"+id+"/json", nil, &insp); err != nil {
		return false
	}
	for net, ep := range insp.NetworkSettings.Networks {
		if !mine[net] {
			continue
		}
		for _, a := range ep.Aliases {
			if a == name {
				return true
			}
		}
		for _, d := range ep.DNSNames {
			if d == name {
				return true
			}
		}
	}
	return false
}

// swarmOwner describes the NON-signpost Swarm service that owns a name — the
// facts the takeover needs to park it (or to refuse precisely).
type swarmOwner struct {
	id       string
	name     string // the service's own Spec.Name
	replicas int
	global   bool
	viaAlias bool // owns the name as a network ALIAS, not as its service name
}

// swarmNameOwner returns the NON-signpost Swarm service that already owns name
// — by its service name (the cluster-wide resolvable name) or a network alias —
// or nil. GET /services lists the WHOLE cluster from a manager, so it also
// catches a real service whose tasks run on other nodes (which nameOwners'
// container scan cannot see). Serving on top would shadow it in DNS.
func swarmNameOwner(name string, self selfInfo) *swarmOwner {
	// Scope to networks WE are on: a service on an overlay we don't share doesn't
	// resolve for our workloads, so serving `name` on our overlay would not shadow
	// it (the container path already scopes this way). `mine` holds our overlays'
	// names AND ids — a service spec's Network Target may be either. If an id
	// lookup fails we can't scope reliably, so fall back to the old cluster-wide
	// check (over-refuse safely) rather than risk missing a real collision.
	mine := map[string]bool{}
	scoped := true
	for _, n := range self.overlayNets() {
		mine[n] = true
		var ni struct {
			Id string `json:"Id"`
		}
		if _, err := dockerAPI("GET", "/networks/"+n, nil, &ni); err == nil && ni.Id != "" {
			mine[ni.Id] = true
		} else {
			scoped = false
		}
	}
	var list []struct {
		ID   string `json:"ID"`
		Spec struct {
			Name   string            `json:"Name"`
			Labels map[string]string `json:"Labels"`
			Mode   struct {
				Replicated struct {
					Replicas int `json:"Replicas"`
				} `json:"Replicated"`
				Global *struct{} `json:"Global"`
			} `json:"Mode"`
			TaskTemplate struct {
				Networks []struct {
					Target  string   `json:"Target"`
					Aliases []string `json:"Aliases"`
				} `json:"Networks"`
			} `json:"TaskTemplate"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services", nil, &list); err != nil {
		return nil // can't tell — Verify is the backstop
	}
	for _, s := range list {
		if s.Spec.Labels["plug.signpost"] == "1" {
			continue // our own signpost services don't count
		}
		shared := !scoped // if we couldn't resolve our net ids, assume shared (safe)
		if scoped {
			for _, n := range s.Spec.TaskTemplate.Networks {
				if mine[n.Target] {
					shared = true
					break
				}
			}
		}
		if !shared {
			continue // no shared network — it can't shadow our name
		}
		// The service's own name resolves on every network it's attached to, and
		// an alias resolves on its network — either collides once a network is shared.
		owns, viaAlias := s.Spec.Name == name, false
		if !owns {
			for _, n := range s.Spec.TaskTemplate.Networks {
				for _, a := range n.Aliases {
					if a == name {
						owns, viaAlias = true, true
						break
					}
				}
			}
		}
		if owns {
			return &swarmOwner{
				id:       s.ID,
				name:     s.Spec.Name,
				replicas: s.Spec.Mode.Replicated.Replicas,
				global:   s.Spec.Mode.Global != nil,
				viaAlias: viaAlias,
			}
		}
	}
	return nil
}

// scaleService sets a Swarm service's replica count, round-tripping the full
// Spec (the update API replaces the whole Spec — a partial one would strip
// fields) at the version the read returned.
func scaleService(idOrName string, replicas int) error {
	var s struct {
		Version struct {
			Index int `json:"Index"`
		} `json:"Version"`
		Spec map[string]any `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+idOrName, nil, &s); err != nil {
		return err
	}
	s.Spec["Mode"] = map[string]any{"Replicated": map[string]any{"Replicas": replicas}}
	_, err := dockerAPI("POST", "/services/"+idOrName+"/update?version="+strconv.Itoa(s.Version.Index), s.Spec, nil)
	return err
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
		// non-attachable overlay off a Swarm manager), so the signpost has
		// nowhere to carry the alias.
		answer("error: the agent is on no network a signpost can join — put it on the " +
			"application network (an attachable overlay, or the Compose network your services share)")
	}
	// A leftover signpost (a crashed session's, or a re-run) may carry a parking
	// receipt: restore it FIRST, then re-detect. One restore path — the takeover
	// below re-parks with a fresh receipt; no label merging across sessions.
	if err := restoreContainerParked(name); err != nil {
		answer("error: restoring what the previous %s session parked: %v", name, err)
	}
	owners := nameOwners(name, nets)
	receipt := make([]string, 0, len(owners))
	for _, o := range owners {
		receipt = append(receipt, o.id)
	}
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
			parkedContainersLabel: strings.Join(receipt, ","),
		},
		"HostConfig":       map[string]any{"NetworkMode": nets[0]},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{nets[0]: endpoints[nets[0]]}},
	}
	var created struct {
		Id string `json:"Id"`
	}
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
	// Park AFTER the signpost is live: a brief both-in-DNS overlap is benign
	// round-robin, whereas a no-record gap would leak the lookup to the upstream
	// resolver (bench-proven on Swarm's embedded DNS).
	for i, o := range owners {
		if _, err := dockerAPI("POST", "/containers/"+o.id+"/stop?t=10", nil, nil); err != nil {
			for _, r := range owners[:i] { // roll the partial park back
				_, _ = dockerAPI("POST", "/containers/"+r.id+"/start", nil, nil)
			}
			_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
			answer("error: parking %q (stopping %s): %v", name, o.name, err)
		}
	}
	if len(owners) > 0 {
		answer("dynamic parked")
	}
	answer("dynamic")
}

// restoreContainerParked restarts whatever a previous session's signpost parked
// (its receipt label), then removes that signpost. No signpost → nothing to do.
// Restore-then-delete keeps the name resolving throughout: the real containers
// come back while the signpost still answers, then the signpost goes.
func restoreContainerParked(name string) error {
	var insp struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if code, err := dockerAPI("GET", "/containers/"+signpostName(name)+"/json", nil, &insp); err != nil {
		if code == 404 {
			return nil
		}
		return err
	}
	restartParkedContainers(insp.Config.Labels[parkedContainersLabel])
	if code, err := dockerAPI("DELETE", "/containers/"+signpostName(name)+"?force=1", nil, nil); err != nil && code != 404 {
		return err
	}
	return nil
}

// restartParkedContainers starts every id in a receipt, best-effort: a container
// that was removed meanwhile (404) or is already running (304) is fine.
func restartParkedContainers(receipt string) {
	for _, id := range strings.Split(receipt, ",") {
		if id = strings.TrimSpace(id); id != "" {
			_, _ = dockerAPI("POST", "/containers/"+id+"/start", nil, nil)
		}
	}
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
	if serviceIsGlobal(self.service) {
		answer("error: the plug agent runs in GLOBAL mode — plug -s needs a single replica (deploy it as mode: replicated, replicas: 1)")
	}
	nets := self.overlayNets()
	if len(nets) == 0 {
		// Only ingress/bridge — nothing to publish an alias on.
		answer("error: the agent is on no overlay network — attach it to the overlay your " +
			"services use, otherwise the name cannot resolve for them")
	}
	// A leftover signpost service (a crashed session's, or a re-run) may carry a
	// parking receipt: restore it FIRST, then re-detect — one restore path, and
	// the takeover below re-parks with a fresh receipt.
	if err := restoreServiceParked(name); err != nil {
		answer("error: restoring what the previous %s session parked: %v", name, err)
	}
	// A real service with this name (anywhere in the cluster) must keep it: the
	// container-scan nameOwners can't see Swarm services, so check them explicitly.
	own := swarmNameOwner(name, self)
	if own != nil {
		if own.global {
			answer("error: %q runs in GLOBAL mode — plug cannot park it (no replica count to restore). Remove it instead: docker service rm %s.", own.name, own.name)
		}
		// A Swarm STACK names its services <stack>_<svc> and carries the short
		// name as a network alias — parking that is exactly the use case (same
		// logical service, stack-prefixed). Refuse only a foreign alias: a
		// service whose own name is unrelated would lose it as collateral.
		if own.viaAlias && !strings.HasSuffix(own.name, "_"+name) {
			answer("error: %q is a network ALIAS of service %q — parking that service would take its own name down too. Remove the alias instead.", name, own.name)
		}
	}

	var attach []map[string]any
	for _, n := range nets {
		attach = append(attach, map[string]any{"Target": n, "Aliases": []string{name}})
	}
	labels := map[string]string{
		"plug.signpost":       "1",
		"plug.signpost.owner": self.owner(),
	}
	if own != nil { // the parking receipt — how unserve/gc restore it
		labels[parkedServiceLabel] = own.name
		labels[parkedReplicasLabel] = strconv.Itoa(max(own.replicas, 1))
	}
	spec := map[string]any{
		"Name":   signpostName(name),
		"Labels": labels,
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
	if own != nil {
		// Park AFTER the signpost exists: a brief both-in-DNS overlap is benign
		// round-robin, whereas a no-record gap forwards the lookup to the upstream
		// resolver (bench-proven on the embedded DNS).
		if err := scaleService(own.id, 0); err != nil {
			_, _ = dockerAPI("DELETE", "/services/"+signpostName(name), nil, nil)
			answer("error: parking %q (scaling %s to 0): %v", name, own.name, err)
		}
		answer("dynamic parked")
	}
	answer("dynamic")
}

// restoreServiceParked scales back whatever a previous session's signpost
// service parked (its receipt labels), then removes that signpost. Scale-back
// first, delete second — the name keeps resolving throughout.
func restoreServiceParked(name string) error {
	var s struct {
		Spec struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Spec"`
	}
	if code, err := dockerAPI("GET", "/services/"+signpostName(name), nil, &s); err != nil {
		if code == 404 || code == 503 { // absent, or not a manager (no service shape here)
			return nil
		}
		return err
	}
	scaleBackParkedService(s.Spec.Labels)
	if code, err := dockerAPI("DELETE", "/services/"+signpostName(name), nil, nil); err != nil && code != 404 {
		return err
	}
	return nil
}

// scaleBackParkedService restores the replica count a receipt recorded,
// best-effort: a service removed meanwhile is fine.
func scaleBackParkedService(labels map[string]string) {
	svc := labels[parkedServiceLabel]
	if svc == "" {
		return
	}
	n, err := strconv.Atoi(labels[parkedReplicasLabel])
	if err != nil || n < 1 {
		n = 1
	}
	_ = scaleService(svc, n)
}

func dockerUnserve(name string) {
	// Drop whichever shape exists — restoring anything its receipt parked FIRST
	// (scale-back / restart, then delete: the name resolves throughout). The
	// container shape is always meaningful; the service shape only on a Swarm
	// manager (off one, /services/* answers 503 — not a real failure). A real
	// failure on either shape is surfaced (a swallowed error would leak the
	// signpost or leave the parked service down).
	if err := restoreContainerParked(name); err != nil {
		answer("error: removing the %s signpost: %v", name, err)
	}
	if swarmManager() {
		if err := restoreServiceParked(name); err != nil {
			answer("error: removing the %s signpost service: %v", name, err)
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
				// An orphaned signpost's receipt is a takeover that never got
				// restored (the session died with the agent) — restore it now,
				// then sweep the signpost.
				restartParkedContainers(c.Labels[parkedContainersLabel])
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
				scaleBackParkedService(s.Spec.Labels) // undo the orphan's takeover
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

// serviceIsGlobal reports whether a Swarm service runs in GLOBAL mode (one task
// per node). serviceReplicas can't see that (global has no Replicated block, so
// it reads as 1), yet the VIP then spreads across nodes and the session's single
// remote-forward task is missed intermittently — so -s must refuse it too.
func serviceIsGlobal(name string) bool {
	var s struct {
		Spec struct {
			Mode struct {
				Global *struct{} `json:"Global"`
			} `json:"Mode"`
		} `json:"Spec"`
	}
	if _, err := dockerAPI("GET", "/services/"+name, nil, &s); err != nil {
		return false
	}
	return s.Spec.Mode.Global != nil
}

func urlEscape(s string) string {
	return strings.NewReplacer("{", "%7B", "}", "%7D", `"`, "%22", "[", "%5B", "]", "%5D", ",", "%2C", ":", "%3A", "=", "%3D").Replace(s)
}

// ---- kubernetes backend (RBAC applied — part of deploy/plug-k8s.yaml) ----

const k8sSA = "/var/run/secrets/kubernetes.io/serviceaccount"

func k8sAvailable() bool {
	_, err := os.Stat(k8sSA + "/token")
	return err == nil
}

func k8sAPI(method, path string, body any, out any) (int, error) {
	return k8sDo(method, path, "application/json", body, out)
}

// k8sMergePatch applies an RFC 7386 JSON merge patch — how the takeover
// repoints a real Service's selector (and how the restore puts it back): object
// keys merge (null deletes a key), arrays replace whole.
func k8sMergePatch(path string, body any) (int, error) {
	return k8sDo("PATCH", path, "application/merge-patch+json", body, nil)
}

func k8sDo(method, path, contentType string, body any, out any) (int, error) {
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
		req.Header.Set("Content-Type", contentType)
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
		var e struct {
			Message string `json:"message"`
		}
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

// k8sParkedAnn is the k8s parking receipt: the takeover repoints the REAL
// Service at the agent and stores its original selector+ports here, on the
// object itself — so the restore (unserve or boot gc) survives any agent crash.
// The annotation is written ONLY when absent: a crashed takeover session leaves
// the Service already pointing at plug, and re-saving would overwrite the
// original with {app: plug}, losing the way back.
const k8sParkedAnn = "plug.softwarity.io/parked"

// k8sReceipt is what the annotation stores — everything the restore re-patches.
type k8sReceipt struct {
	Selector map[string]string `json:"selector"`
	Ports    json.RawMessage   `json:"ports"`
}

// selectorPatch builds the merge-patch value that REPLACES a selector: RFC 7386
// merges maps key-by-key, so every key of the current selector that the target
// doesn't carry must be explicitly nulled or it would survive the patch (and a
// half-merged selector matches nothing).
func selectorPatch(target, current map[string]string) map[string]any {
	p := map[string]any{}
	for k := range current {
		p[k] = nil
	}
	for k, v := range target {
		p[k] = v
	}
	return p
}

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
		// No RBAC → the opt-in was never applied, so it cannot create the name.
		answer("error: the agent may not manage Services in namespace %s — apply the RBAC (deploy/plug-k8s.yaml)", ns)
	case code == 409:
		// The name exists. A previous plug session's leftover is replaced; a REAL
		// Service keeps its name — unless takeover, which repoints it at the agent
		// for the session (selector+ports), receipt in an annotation on itself.
		var existing struct {
			Metadata struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Selector map[string]string `json:"selector"`
				Ports    json.RawMessage   `json:"ports"`
			} `json:"spec"`
		}
		_, gerr := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing)
		if gerr != nil || existing.Metadata.Labels[k8sManaged] != "plug" {
			if gerr == nil {
				receipt := existing.Metadata.Annotations[k8sParkedAnn]
				if receipt == "" { // first takeover of this Service — save the way back
					b, merr := json.Marshal(k8sReceipt{Selector: existing.Spec.Selector, Ports: existing.Spec.Ports})
					if merr != nil {
						answer("error: recording %q's original spec: %v", name, merr)
					}
					receipt = string(b)
				}
				patch := map[string]any{
					"metadata": map[string]any{"annotations": map[string]any{k8sParkedAnn: receipt}},
					"spec": map[string]any{
						"selector": selectorPatch(map[string]string{"app": "plug"}, existing.Spec.Selector),
						"ports":    []map[string]any{{"port": p, "targetPort": p}},
					},
				}
				if _, perr := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); perr != nil {
					answer("error: parking the Service %q (repointing it at the agent): %v", name, perr)
				}
				answer("dynamic parked")
			}
			answer("error: the Service %q exists but plug cannot read it — remove it, or grant the agent access: kubectl delete service %s", name, name)
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

// k8sRestoreParked undoes a takeover on one Service: re-patch its original
// selector+ports from the receipt annotation, and drop the annotation. Reports
// whether the Service was parked at all.
func k8sRestoreParked(ns, name string, ann map[string]string, current map[string]string) (bool, error) {
	raw := ann[k8sParkedAnn]
	if raw == "" {
		return false, nil
	}
	var r k8sReceipt
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return true, fmt.Errorf("unreadable parking receipt on %q: %v", name, err)
	}
	patch := map[string]any{
		"metadata": map[string]any{"annotations": map[string]any{k8sParkedAnn: nil}},
		"spec": map[string]any{
			"selector": selectorPatch(r.Selector, current),
			"ports":    r.Ports,
		},
	}
	if _, err := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); err != nil {
		return true, err
	}
	return true, nil
}

func k8sUnserve(name string) {
	ns := k8sNamespace()
	var existing struct {
		Metadata struct {
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if _, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing); err != nil {
		answer("ok") // absent (or no RBAC) — nothing to drop
	}
	if existing.Metadata.Labels[k8sManaged] == "plug" {
		// Ours: the plug-created Service goes with the session.
		if _, err := k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil); err != nil {
			answer("error: %v", err)
		}
		answer("ok")
	}
	// A REAL Service we parked (takeover): restore it from its receipt.
	parked, err := k8sRestoreParked(ns, name, existing.Metadata.Annotations, existing.Spec.Selector)
	if err != nil {
		answer("error: restoring the Service %q: %v", name, err)
	}
	if parked {
		answer("ok")
	}
	answer("ok") // not ours, not parked — nothing to drop
}

func k8sGC() {
	ns := k8sNamespace()
	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Selector map[string]string `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	// One un-filtered list serves both sweeps: parked REAL Services (annotation —
	// restore them) and plug-created ones (label — delete them).
	if _, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services", nil, &list); err != nil {
		return
	}
	for _, s := range list.Items {
		if s.Metadata.Labels[k8sManaged] == "plug" {
			_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+s.Metadata.Name, nil, nil)
			continue
		}
		_, _ = k8sRestoreParked(ns, s.Metadata.Name, s.Metadata.Annotations, s.Spec.Selector)
	}
}
