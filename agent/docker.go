package agent

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Everything the agent does through the Docker Engine API, for a plain
// docker-compose cluster.
//
// Moved out of main.go with the Kubernetes half, for the same reason: three
// orchestrators in one file of three thousand lines, interleaved. dockerAPI is
// shared with the Swarm side, which speaks the same API, and stays here because
// that is where its shape belongs.

// dockerPlainSelfUpdate (Compose / plain `docker run`): pull the deployed tag
// and compare image ids. A container cannot recreate ITSELF, so when something
// newer landed the answer carries the one command the caller runs — with the
// image already local, that recreate is instant.
func dockerPlainSelfUpdate(self selfInfo, decide func(string) (string, string, string)) {
	ver := localVersion()
	img := self.image
	if strings.HasPrefix(img, "sha256:") {
		answer("error: the agent was started from an image ID, not a tag — recreate it from a tag (softwarity/plug:latest) so updates can pull")
	}
	target, plan, note := decide(img)
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

func dockerAvailable() bool {
	_, err := os.Stat(dockerSock)
	return err == nil
}

// dockerArchive GETs the tar of a path inside a container - the Engine's
// /containers/{id}/archive, which reads the container's filesystem whether it
// runs or not, so it works on the STOPPED container a takeover parks. The tar's
// members are rooted at the path's basename; rerootTar puts them back on their
// absolute path for the client. Raw bytes, not JSON, so it cannot go through
// dockerAPI.
func dockerArchive(id, path string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", "http://docker/containers/"+id+"/archive?path="+url.QueryEscape(path), nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := dockerClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("archive %s: %s", path, strings.TrimSpace(string(data)))
	}
	return data, 200, nil
}

// dockerExec runs cmd in a RUNNING container and returns its stdout. It reaches
// what the archive API cannot: a tmpfs mount (a Swarm secret lives in one) is
// not in the container's filesystem layer, so it must be read from INSIDE the
// running container. The attach stream is multiplexed - 8-byte frame headers,
// stream 1 stdout - since no TTY is asked (the payload is a binary tar).
func dockerExec(id string, cmd []string) ([]byte, error) {
	var created struct {
		ID string `json:"Id"`
	}
	body := map[string]any{"AttachStdout": true, "AttachStderr": false, "Cmd": cmd}
	if code, err := dockerAPI("POST", "/containers/"+id+"/exec", body, &created); err != nil || code != 201 || created.ID == "" {
		return nil, fmt.Errorf("exec create (code %d): %v", code, err)
	}
	req, err := http.NewRequest("POST", "http://docker/exec/"+created.ID+"/start", strings.NewReader(`{"Detach":false,"Tty":false}`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := dockerClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return demuxDockerStdout(resp.Body)
}

// demuxDockerStdout returns the stdout bytes of Docker's multiplexed attach
// stream: each frame is [stream, 0,0,0, size(4, big-endian)] then size bytes,
// stream 1 is stdout and 2 is stderr.
func demuxDockerStdout(r io.Reader) ([]byte, error) {
	var out []byte
	var h [8]byte
	for {
		if _, err := io.ReadFull(r, h[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return out, err
		}
		n := binary.BigEndian.Uint32(h[4:])
		if n == 0 {
			continue
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return out, err
		}
		if h[0] == 1 { // stdout
			out = append(out, buf...)
		}
	}
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
	return readAPIReply(resp.StatusCode, resp.Status, data, out, true)
}

// containerIDFromMount reads THIS container's id from the Docker-bind-mounted
// paths in mountinfo (`…/containers/<id>/{resolv.conf,hostname,hosts}`), which
// the kernel exposes and is therefore authoritative, unlike the hostname (a
// stack can override it, and the inspect would then be a DIFFERENT
// container's). The segment is anchored on `/containers/<64-hex>/`, NOT any
// 64-hex: the overlay layer hashes elsewhere in mountinfo are also 64-hex and
// must not be picked. "" when the pattern isn't present (the hostname stands).
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

// dockerSelf identifies OUR container. A var so a test can hand the backends an
// identity directly: the real one asks the daemon about THIS process's
// hostname, which no fake Engine can answer for.
//
// Remembered once it has answered: a verb asks it two or three times (the
// witness, the serve, the self-update), the serve process once a minute, and
// the container an agent runs in does not change under it. Only a SUCCESS is
// kept: the serve process lives for weeks, and a daemon that hiccups at boot
// must not leave every later sweep unable to identify the agent.
var dockerSelf = memoize(dockerSelfInspect)

// memoize returns f with its first successful answer kept for every later
// call. A failure is returned and NOT kept, so the next call asks again.
func memoize[T any](f func() (T, error)) func() (T, error) {
	var (
		mu  sync.Mutex
		val T
		ok  bool
	)
	return func() (T, error) {
		mu.Lock()
		defer mu.Unlock()
		if ok {
			return val, nil
		}
		v, err := f()
		if err != nil {
			return v, err
		}
		val, ok = v, true
		return val, nil
	}
}

// dockerSelfInspect is that lookup. The hostname is the short container id by
// default and resolves directly — the proven path. Only if that GET fails (a
// stack that overrode the hostname) do we fall back to the authoritative id
// parsed from mountinfo.
func dockerSelfInspect() (selfInfo, error) {
	var s selfInfo
	var insp struct {
		Id     string `json:"Id"`
		Name   string `json:"Name"`
		Image  string `json:"Image"` // the resolved image ID (sha256:…)
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress         string `json:"IPAddress"`
				GlobalIPv6Address string `json:"GlobalIPv6Address"`
			} `json:"Networks"`
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
	s.id = insp.Id
	s.name = strings.TrimPrefix(insp.Name, "/")
	s.image = insp.Config.Image
	s.imageID = insp.Image
	s.compose = insp.Config.Labels["com.docker.compose.service"]
	s.project = insp.Config.Labels["com.docker.compose.project"]
	s.service = insp.Config.Labels["com.docker.swarm.service.name"]
	s.serviceID = insp.Config.Labels["com.docker.swarm.service.id"]
	for n, ep := range insp.NetworkSettings.Networks {
		if undnsNetwork[n] {
			continue
		}
		if app, att, ov := netKind(n); app {
			var addrs []string
			for _, a := range []string{ep.IPAddress, ep.GlobalIPv6Address} {
				if a != "" {
					addrs = append(addrs, a)
				}
			}
			s.nets = append(s.nets, netRef{name: n, attachable: att, overlay: ov, addrs: addrs})
		}
	}
	return s, nil
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

// dockerServe picks the signpost shape. The agent runs as a Swarm SERVICE (it
// has a service name) AND this node can create services (a manager) → the
// signpost is a SERVICE, which joins the stack's overlay whether or not it is
// attachable. Otherwise (Compose, plain `docker run`, or a non-manager) it is a
// standalone CONTAINER, which needs a bridge or an attachable overlay.
func dockerServe(name string, pairs []portPair) {
	self, err := dockerSelf()
	if err != nil {
		answer("error: %v", err)
	}
	if self.service != "" && swarmManager() {
		swarmServe(name, pairs, self)
	}
	containerServe(name, pairs, self)
}

// containerServe runs the signpost as a standalone container — needs a network
// it can actually join (a bridge, or an attachable overlay).
func containerServe(name string, pairs []portPair, self selfInfo) {
	nets := self.attachableNets()
	if len(nets) == 0 {
		// Nothing a standalone container can join (only bridge/host, or a
		// non-attachable overlay off a Swarm manager), so the signpost has
		// nowhere to carry the alias.
		answer("error: the agent is on no network a signpost can join — put it on the " +
			"application network (an attachable overlay, or the Compose network your services share)")
	}
	// A signpost already carrying this name may belong to a LIVE session — its
	// relay port still answers on this agent — and then the name is taken; a
	// dead port is a crashed session's leftover, swept below.
	var insp struct {
		Config struct {
			Entrypoint []string          `json:"Entrypoint"`
			Labels     map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if code, err := dockerAPI("GET", "/containers/"+signpostName(name)+"/json", nil, &insp); err == nil && code == 200 {
		// Whose signpost is this? A container name is HOST-wide, so two agents
		// on one host — two Compose stacks, each with its own plug — collide on
		// `plug-sp-<name>`. The liveness probe below cannot tell them apart: it
		// dials 127.0.0.1 in OUR netns, where the other agent's port never
		// answers, so its LIVE signpost reads as a leftover and gets swept. The
		// gc has always checked this label; the serve path never did.
		if o := insp.Config.Labels[signpostOwnerLabel]; o != "" && o != self.owner() && ownerAlive(o, false) {
			answer("error: %q is served here by another plug agent (%s), which is still running — "+
				"two agents on one host cannot both own a name. Use a different name, or stop that agent.", name, o)
		}
		// Ask the session's own address, not 127.0.0.1: past that owner check the
		// signpost may still belong to a SIBLING replica of this same deployment
		// (same role, same owner label), whose forward answers on the network and
		// never in this container's loopback.
		if own := signpostOwner(insp.Config.Labels, insp.Config.Entrypoint); sessionLive(own) {
			answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
		}
	}
	// A leftover signpost (a crashed session's, or a re-run) may carry a parking
	// receipt: restore it FIRST, then re-detect. One restore path — the takeover
	// below re-parks with a fresh receipt; no label merging across sessions.
	if err := restoreContainerParked(name); err != nil {
		answer("error: restoring what the previous %s session parked: %v", name, err)
	}
	owners := nameOwners(name, nets)
	// The agent answering to the name itself (its container name, or an alias
	// such as its Compose service name) is the one owner a takeover must never
	// park: stopping it stops the forward the session relies on, and the
	// receipt goes down with the agent that would have read it.
	if o := agentAmongOwners(owners, self); o != nil {
		answer("error: %q is this agent's own container (%s): plug cannot park the agent that serves the session. "+
			"Serve a different name", name, o.name)
	}
	receipt := make([]string, 0, len(owners))
	for _, o := range owners {
		receipt = append(receipt, o.id)
	}
	endpoints := map[string]any{}
	for _, n := range nets {
		endpoints[n] = map[string]any{"Aliases": []string{name}}
	}
	body := map[string]any{
		"Image":      signpostImage(self.image),
		"Entrypoint": signpostArgs(pairs, self.relayTarget()),
		"Labels": map[string]string{
			signpostLabel:         "1",
			signpostNameLabel:     name,
			signpostOwnerLabel:    self.owner(),
			sessionOwnerLabel:     sessionOwner(self.relayTarget(), pairs),
			parkedContainersLabel: strings.Join(receipt, ","),
		},
		// Restart it if it ever dies: the Swarm signpost has RestartPolicy any
		// and a k8s pod is restarted by its Deployment — a standalone container
		// had nothing, so one crash took the cluster name down for the rest of
		// the session. `unless-stopped` so the session teardown's stop is final.
		"HostConfig": map[string]any{
			"NetworkMode":   nets[0],
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
		},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{nets[0]: endpoints[nets[0]]}},
	}
	// The alias must exist on EVERY network the agent is on (workloads may look
	// from any of them).
	id, step, network, err := createAttachedContainer(signpostName(name), body, nets[1:], endpoints)
	switch {
	case err == nil:
	case step == stepCreate:
		answer("error: creating the %s signpost: %v", name, err)
	case step == stepAttach:
		answer("error: attaching the %s signpost to %s: %v", name, network, err)
	default:
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
			_, _ = dockerAPI("DELETE", "/containers/"+id+"?force=1", nil, nil)
			answer("error: parking %q (stopping %s): %v", name, o.name, err)
		}
	}
	if len(owners) > 0 {
		answer("dynamic parked")
	}
	answer("dynamic")
}

// createStep names where createAttachedContainer stopped, so the caller can
// phrase its refusal: the signpost and the mount helper say different things
// about the same failure.
type createStep int

const (
	stepCreate createStep = iota
	stepAttach
	stepStart
)

// createAttachedContainer creates a container (its first network in the body's
// HostConfig and NetworkingConfig), joins it to every further network with the
// given endpoint config, and starts it. Past the create, a failure removes the
// container before returning: a container that exists and does not run, or
// runs on half its networks, would carry a name it cannot answer for. Returns
// the id, or the step that failed, the network of a failed attach, and why.
func createAttachedContainer(name string, body map[string]any, more []string, endpoints map[string]any) (id string, step createStep, network string, err error) {
	var created struct {
		Id string `json:"Id"`
	}
	if _, err := dockerAPI("POST", "/containers/create?name="+name, body, &created); err != nil {
		return "", stepCreate, "", err
	}
	for _, n := range more {
		if _, err := dockerAPI("POST", "/networks/"+n+"/connect",
			map[string]any{"Container": created.Id, "EndpointConfig": endpoints[n]}, nil); err != nil {
			_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
			return "", stepAttach, n, err
		}
	}
	if _, err := dockerAPI("POST", "/containers/"+created.Id+"/start", nil, nil); err != nil {
		_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
		return "", stepStart, "", err
	}
	return created.Id, 0, "", nil
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

// dockerFilters renders a Docker list filter as the API's query value: the
// JSON object of name to values that /containers/json, /services, /tasks and
// /secrets take, escaped for the URL. One renderer, because the filters used
// to be concatenated by hand at five sites with a partial escaper beside the
// standard one, and a label value with a character the escaper did not know
// would have listed nothing, silently.
func dockerFilters(f map[string][]string) string {
	b, _ := json.Marshal(f)
	return url.QueryEscape(string(b))
}

// dockerContainerRec is a container as GET /containers/json lists it.
type dockerContainerRec struct {
	Id     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
}

// swarmObjectRec is a service or a secret as its list endpoint reports it:
// both carry their name and labels in a Spec.
type swarmObjectRec struct {
	ID   string `json:"ID"`
	Spec struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	} `json:"Spec"`
}

// dockerContainersLabelled lists every container, running or not, carrying
// label=1: the signposts, or the mount helpers.
func dockerContainersLabelled(label string) ([]dockerContainerRec, error) {
	var list []dockerContainerRec
	_, err := dockerAPI("GET", "/containers/json?all=1&filters="+dockerFilters(map[string][]string{"label": {label + "=1"}}), nil, &list)
	return list, err
}

// swarmObjectsLabelled lists the services (kind "services") or the secrets
// (kind "secrets") carrying label=1. Only answered on a manager: off one the
// endpoint says 503, which the callers treat as "nothing to sweep".
func swarmObjectsLabelled(kind, label string) ([]swarmObjectRec, error) {
	var list []swarmObjectRec
	_, err := dockerAPI("GET", "/"+kind+"?filters="+dockerFilters(map[string][]string{"label": {label + "=1"}}), nil, &list)
	return list, err
}

// dockerGC sweeps, at agent boot and every minute after, THIS agent's own
// orphaned signposts (an agent restart leaves its sessions' signposts running).
// A signpost is ours if its owner label is our current name OR its owner
// container no longer exists: the latter covers Swarm, where the agent's
// container name churns on restart, so the old signposts' owner never equals
// the new name but their owner container is gone. This leaves a CO-LOCATED
// other agent's live signposts (owner still running) untouched; the
// pure-shared-network scan used to wipe those. The rules are the sweep's
// (sweep.go); what is here is how a signpost reads and acts on each backend.
func dockerGC() {
	self, err := dockerSelf()
	if err != nil {
		gcNote("cannot identify this agent (%v) — leftovers from crashed sessions were NOT swept", err)
		return
	}
	mine := self.owner()
	swarm := swarmManager()
	sweep(dockerSignpostContainers(mine, swarm))
	// Swarm-service signposts (only reachable on a manager).
	if swarm {
		sweep(dockerSignpostServices(mine))
	}
}

// dockerSignpostContainers is the standalone-container signposts as the sweep
// sees them. A container never lingers: its relay target is baked into its
// entrypoint, so there is no address worth keeping.
func dockerSignpostContainers(mine string, swarm bool) sweepTarget {
	return func() []sweepItem {
		list, err := dockerContainersLabelled(signpostLabel)
		if err != nil {
			return nil
		}
		items := make([]sweepItem, 0, len(list))
		for _, c := range list {
			sp := c.Id[:12]
			if len(c.Names) > 0 {
				sp = strings.TrimPrefix(c.Names[0], "/")
			}
			id, owner, session, receipt := c.Id, c.Labels[signpostOwnerLabel], c.Labels[sessionOwnerLabel], c.Labels[parkedContainersLabel]
			items = append(items, sweepItem{
				key:  "container:" + id,
				live: func() bool { return sessionLive(session) },
				held: func() bool { return owner != mine && ownerAlive(owner, swarm) },
				// An orphaned signpost's receipt is a takeover that never got
				// restored (the session died with the agent): restore it now,
				// and the sweep removes the signpost only once that succeeded.
				restore: func() (string, error) {
					if failed := restartParkedContainers(receipt); len(failed) > 0 {
						return "", fmt.Errorf("could not restart %s while cleaning up the %s signpost - keeping it "+
							"so its receipt survives; the sweep retries every minute, or start them by hand",
							strings.Join(failed, ", "), sp)
					}
					return fmt.Sprintf("restarted what the %s signpost had parked, after an earlier failure", sp), nil
				},
				drop: func() { _, _ = dockerAPI("DELETE", "/containers/"+id+"?force=1", nil, nil) },
			})
		}
		return items
	}
}

// dockerSignpostServices is the Swarm-service signposts as the sweep sees them:
// the shape that lingers (its VIP is worth keeping), whose receipt is a scaled
// service, and whose park left a secret stash to drop with the signpost.
func dockerSignpostServices(mine string) sweepTarget {
	return func() []sweepItem {
		list, err := swarmObjectsLabelled("services", signpostLabel)
		if err != nil {
			return nil
		}
		items := make([]sweepItem, 0, len(list))
		for _, s := range list {
			id, name, labels := s.ID, s.Spec.Name, s.Spec.Labels
			owner, session, parked := labels[signpostOwnerLabel], labels[sessionOwnerLabel], labels[parkedServiceLabel]
			remove := func() { _, _ = dockerAPI("DELETE", "/services/"+id, nil, nil) }
			items = append(items, sweepItem{
				key:   "service:" + id,
				stamp: labels[lingerLabel],
				// A sibling task's session, same as the container shape: its
				// forward answers on the overlay, and the owner label cannot tell
				// it apart from ours because both tasks report the same service.
				live: func() bool { return sessionLive(session) },
				held: func() bool { return owner != mine && ownerAlive(owner, true) },
				// Same rule as restoreServiceParked: the receipt is in the
				// signpost's labels, so a scale-back that failed keeps the
				// signpost for the next sweep rather than leaving a service at
				// zero replicas with nothing recording that a session put it there.
				restore: func() (string, error) {
					if err := scaleBackParkedService(labels); err != nil {
						return "", fmt.Errorf("could not scale %q back up while cleaning up the %s signpost (%v) - keeping it "+
							"so its receipt survives; the sweep retries every minute, or scale it by hand",
							parked, name, err)
					}
					return fmt.Sprintf("scaled %q back up, after an earlier failure", parked), nil
				},
				drop: remove,
				retire: func() {
					remove()
					// The secrets stashed at park are the parked service's, and
					// it is running again: drop them, as restoreServiceParked
					// does. The stash is keyed by the served name, which the
					// label carries whole (the object's name may have been cut
					// to fit).
					if n := signpostServedName(name, labels); n != "" {
						_ = os.Remove(swarmSecretStash(n))
					}
				},
			})
		}
		return items
	}
}

// sweepExpiredServiceLingers reaps every lingering signpost service past its
// grace, and nothing else: called from a serve, because boot and the minute
// ticker are the only other moments the agent runs any code, and the name
// being served may be the one whose grace just ran out (then the create that
// follows gets a fresh VIP, which is the honest outcome).
func sweepExpiredServiceLingers(mine string) {
	sweep(lingering(dockerSignpostServices(mine)))
}

// containerExists reports whether a container named `name` is present (running
// or not).
func containerExists(name string) bool {
	code, err := dockerAPI("GET", "/containers/"+name+"/json", nil, nil)
	return err == nil || code != 404
}
