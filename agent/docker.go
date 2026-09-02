package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
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

func dockerGC() {
	self, err := dockerSelf()
	if err != nil {
		gcNote("cannot identify this agent (%v) — leftovers from crashed sessions were NOT swept", err)
		return
	}
	mine := self.owner()
	swarm := swarmManager()
	f := `{"label":["` + signpostLabel + `=1"]}`
	// Standalone-container signposts.
	var clist []struct {
		Id     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if _, err := dockerAPI("GET", "/containers/json?all=1&filters="+urlEscape(f), nil, &clist); err == nil {
		for _, c := range clist {
			// Before ownership: is anyone still USING it? The owner label names a
			// role, so every replica of one deployment reads its siblings' live
			// signposts as "mine" and would sweep them at boot - restoring what
			// their sessions parked while those sessions are still serving it.
			if sessionLive(c.Labels[sessionOwnerLabel]) {
				continue
			}
			o := c.Labels[signpostOwnerLabel]
			if o == mine || !ownerAlive(o, swarm) {
				// An orphaned signpost's receipt is a takeover that never got
				// restored (the session died with the agent) — restore it now,
				// then sweep the signpost.
				if failed := restartParkedContainers(c.Labels[parkedContainersLabel]); len(failed) > 0 {
					gcNote("could not restart %s while cleaning up %s — start them by hand",
						strings.Join(failed, ", "), c.Labels[parkedContainersLabel])
				}
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
		now := time.Now()
		for _, s := range slist {
			// The linger rule comes FIRST, before ownership: a lingering
			// signpost is holding an ADDRESS warm, and an agent restart renames
			// the owner — judged by the owner rule alone it would read as an
			// orphan and be swept, killing the very address the linger exists to
			// keep. Within the grace it stays, whoever stamped it; past the
			// grace it goes, whoever stamped it.
			if stamp := s.Spec.Labels[lingerLabel]; stamp != "" {
				if !lingerExpired(stamp, now) {
					continue
				}
				_, _ = dockerAPI("DELETE", "/services/"+s.ID, nil, nil)
				continue
			}
			// A sibling task's session, same as the container shape above: its
			// forward answers on the overlay, and the owner label cannot tell it
			// apart from ours because both tasks report the same service.
			if sessionLive(s.Spec.Labels[sessionOwnerLabel]) {
				continue
			}
			o := s.Spec.Labels[signpostOwnerLabel]
			if o == mine || !ownerAlive(o, swarm) {
				if err := scaleBackParkedService(s.Spec.Labels); err != nil { // undo the orphan's takeover
					gcNote("could not scale %q back up while cleaning up: %v",
						s.Spec.Labels[parkedServiceLabel], err)
				}
				_, _ = dockerAPI("DELETE", "/services/"+s.ID, nil, nil)
			}
		}
	}
}

// containerExists reports whether a container named `name` is present (running
// or not).
func containerExists(name string) bool {
	code, err := dockerAPI("GET", "/containers/"+name+"/json", nil, nil)
	return err == nil || code != 404
}
