package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake Docker Engine, in memory, behind an httptest.Server.
//
// It answers the slice of the Engine API the agent's provisioning paths use
// (containers, Swarm services, secrets, tasks, networks, images, /info) and
// keeps a journal of every request, so a test can assert ORDER (the signpost
// is started before the workload is stopped) and not only the final state. It
// stores no more than the agent reads back: no pulls, no logs, no addresses
// unless seeded, an exec that carries nothing.
//
// Why it exists: three bugs in one week lived in these paths (a parking receipt
// destroyed after a failed restore, a takeover parking the agent itself, a
// ClusterIP lost at restart) and every one of them was found by the e2e suite
// alone, forty minutes per attempt, because dockerClient dialled a Unix socket
// and nothing else. The seams in main.go, docker.go and k8s.go let these tests
// stand a server in for the daemon; the fake is what stands there.
//
// Not covered: image pulls (dockerPull streams), container archives, exec
// output, the daemon's own validation of a spec. A spec the fake accepts may
// still be one a daemon refuses; the e2e suite keeps that.

type fakeContainer struct {
	ID         string
	Name       string
	Image      string
	Labels     map[string]string
	Entrypoint []string
	Env        []string
	User       string
	Running    bool
	Networks   map[string]*fakeEndpoint
	Mounts     []dockerMount    // as inspect reports them
	HostMounts []map[string]any // HostConfig.Mounts as the create carried them
}

type fakeEndpoint struct {
	IPAddress string
	Aliases   []string
}

type fakeService struct {
	ID      string
	Version uint64
	Spec    map[string]any // kept generic: the agent round-trips the full Spec on update
}

type fakeSecret struct {
	ID   string
	Spec map[string]any // Name, Labels, Data
}

type fakeNetwork struct {
	ID         string
	Driver     string
	Ingress    bool
	Attachable bool
}

type fakeEngine struct {
	t   *testing.T
	srv *httptest.Server

	// Everything below is read and written under mu, by the server goroutine
	// and by the test: the race detector sees no happens-before through a
	// socket, so the seeding and the assertions take the lock too.
	mu         sync.Mutex
	swarm      bool // a manager: /services, /secrets and /tasks answer
	seq        int
	containers map[string]*fakeContainer // by id
	services   map[string]*fakeService   // by id
	secrets    map[string]*fakeSecret    // by id
	networks   map[string]*fakeNetwork   // by name
	tasks      []map[string]any
	images     map[string][]string // image name -> RepoDigests
	log        []string            // "METHOD /path?query", in order
	refuse     func(method, path string) (int, string)
}

// newFakeEngine starts the fake and points the agent's Docker client and
// socket check at it for the length of the test.
func newFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	fe := &fakeEngine{
		t:          t,
		containers: map[string]*fakeContainer{},
		services:   map[string]*fakeService{},
		secrets:    map[string]*fakeSecret{},
		networks:   map[string]*fakeNetwork{},
		images:     map[string][]string{},
	}
	fe.srv = httptest.NewServer(fe)
	addr := fe.srv.Listener.Addr().String()
	oldClient, oldSock, oldSA, oldControl := dockerClient, dockerSock, k8sSA, swarmControl
	dockerClient = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}},
	}
	// dockerAvailable is a stat on the socket path: a scratch file stands in.
	// And no ServiceAccount, so k8sAvailable answers no whatever the host has.
	dockerSock = filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(dockerSock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	k8sSA = t.TempDir()
	// The agent remembers the manager probe's answer for the life of the
	// process; this fake is the process's daemon for the life of the test.
	fe.forgetSwarm()
	t.Cleanup(func() {
		dockerClient.CloseIdleConnections()
		fe.srv.Close()
		dockerClient, dockerSock, k8sSA, swarmControl = oldClient, oldSock, oldSA, oldControl
	})
	return fe
}

// ---- the server ----

func (fe *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.log = append(fe.log, r.Method+" "+r.URL.RequestURI())
	p, q := r.URL.Path, r.URL.Query()
	if fe.refuse != nil {
		if code, msg := fe.refuse(r.Method, p); code != 0 {
			jsonReply(w, code, apiMsg(msg))
			return
		}
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	switch {
	case p == "/info":
		state := "inactive"
		if fe.swarm {
			state = "active"
		}
		jsonReply(w, 200, map[string]any{"Swarm": map[string]any{"ControlAvailable": fe.swarm, "LocalNodeState": state}})
	case p == "/containers/json":
		fe.listContainers(w, q)
	case p == "/containers/create" && r.Method == "POST":
		fe.createContainer(w, q.Get("name"), body)
	case strings.HasPrefix(p, "/containers/"):
		fe.containerOp(w, r.Method, strings.TrimPrefix(p, "/containers/"))
	case strings.HasPrefix(p, "/exec/") && r.Method == "POST":
		w.WriteHeader(200) // an exec whose stream carries nothing
	case strings.HasPrefix(p, "/networks/"):
		fe.networkOp(w, r.Method, strings.TrimPrefix(p, "/networks/"), body)
	case strings.HasPrefix(p, "/images/") && strings.HasSuffix(p, "/json"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/images/"), "/json")
		digests, ok := fe.images[name]
		if !ok {
			jsonReply(w, 404, apiMsg("No such image: "+name))
			return
		}
		jsonReply(w, 200, map[string]any{"Id": "sha256:" + fakeDigest(name), "RepoDigests": digests})
	case !fe.swarm:
		// What a non-manager says to every Swarm endpoint, verbatim: the agent
		// reads that 503 as "no service shape here".
		jsonReply(w, 503, apiMsg("This node is not a swarm manager. Use \"docker swarm init\" or \"docker swarm join\" to connect this node to swarm and try again."))
	case p == "/services":
		fe.listServices(w, q)
	case p == "/services/create" && r.Method == "POST":
		fe.createService(w, body)
	case strings.HasPrefix(p, "/services/"):
		fe.serviceOp(w, r.Method, strings.TrimPrefix(p, "/services/"), q, body)
	case p == "/tasks":
		fe.listTasks(w, q)
	case p == "/secrets":
		fe.listSecrets(w, q)
	case p == "/secrets/create" && r.Method == "POST":
		fe.createSecret(w, body)
	case strings.HasPrefix(p, "/secrets/"):
		fe.secretOp(w, r.Method, strings.TrimPrefix(p, "/secrets/"))
	default:
		jsonReply(w, 404, apiMsg("page not found"))
	}
}

func (fe *fakeEngine) listContainers(w http.ResponseWriter, q url.Values) {
	all := q.Get("all") == "1" || q.Get("all") == "true"
	f := parseFilters(q.Get("filters"))
	out := []map[string]any{}
	for _, c := range fe.sortedContainers() {
		if !all && !c.Running || !labelsMatch(c.Labels, f["label"]) {
			continue
		}
		nets := map[string]any{}
		for n := range c.Networks {
			nets[n] = map[string]any{}
		}
		out = append(out, map[string]any{
			"Id": c.ID, "Names": []string{"/" + c.Name}, "Labels": c.Labels, "State": c.state(),
			"NetworkSettings": map[string]any{"Networks": nets},
		})
	}
	jsonReply(w, 200, out)
}

func (fe *fakeEngine) createContainer(w http.ResponseWriter, name string, body map[string]any) {
	if name == "" {
		jsonReply(w, 400, apiMsg("a name is required here"))
		return
	}
	if fe.findContainer(name) != nil {
		jsonReply(w, 409, apiMsg(fmt.Sprintf("Conflict. The container name %q is already in use", "/"+name)))
		return
	}
	c := &fakeContainer{ID: fe.hexID(), Name: name, Labels: map[string]string{}, Networks: map[string]*fakeEndpoint{}}
	c.Image, _ = body["Image"].(string)
	c.Entrypoint = anyStrings(body["Entrypoint"])
	c.Env = anyStrings(body["Env"])
	c.User, _ = body["User"].(string)
	for k, v := range anyMap(body["Labels"]) {
		c.Labels[k], _ = v.(string)
	}
	hc := anyMap(body["HostConfig"])
	if mode, _ := hc["NetworkMode"].(string); mode != "" {
		c.Networks[mode] = &fakeEndpoint{}
	}
	for _, m := range anySlice(hc["Mounts"]) {
		mm := anyMap(m)
		c.HostMounts = append(c.HostMounts, mm)
		src, _ := mm["Source"].(string)
		typ, _ := mm["Type"].(string)
		dst, _ := mm["Target"].(string)
		dm := dockerMount{Type: typ, Source: src, Destination: dst}
		if typ == "volume" {
			dm.Name = src
		}
		c.Mounts = append(c.Mounts, dm)
	}
	for n, ep := range anyMap(anyMap(body["NetworkingConfig"])["EndpointsConfig"]) {
		c.Networks[n] = &fakeEndpoint{Aliases: anyStrings(anyMap(ep)["Aliases"])}
	}
	fe.containers[c.ID] = c
	jsonReply(w, 201, map[string]any{"Id": c.ID, "Warnings": []string{}})
}

func (fe *fakeEngine) containerOp(w http.ResponseWriter, method, rest string) {
	id, op, _ := strings.Cut(rest, "/")
	c := fe.findContainer(id)
	if c == nil {
		jsonReply(w, 404, apiMsg("No such container: "+id))
		return
	}
	switch {
	case method == "GET" && op == "json":
		nets := map[string]any{}
		for n, ep := range c.Networks {
			nets[n] = map[string]any{
				"IPAddress": ep.IPAddress, "GlobalIPv6Address": "", "Aliases": ep.Aliases,
				"DNSNames": append([]string{c.Name}, ep.Aliases...),
			}
		}
		jsonReply(w, 200, map[string]any{
			"Id": c.ID, "Name": "/" + c.Name, "Image": "sha256:" + fakeDigest(c.Image),
			"State":           map[string]any{"Status": c.state(), "Running": c.Running},
			"Config":          map[string]any{"Image": c.Image, "Labels": c.Labels, "Entrypoint": c.Entrypoint, "Env": c.Env},
			"NetworkSettings": map[string]any{"Networks": nets},
			"Mounts":          c.Mounts,
		})
	case method == "POST" && op == "start":
		if c.Running {
			w.WriteHeader(304)
			return
		}
		c.Running = true
		w.WriteHeader(204)
	case method == "POST" && op == "stop":
		if !c.Running {
			w.WriteHeader(304)
			return
		}
		c.Running = false
		w.WriteHeader(204)
	case method == "POST" && op == "exec":
		jsonReply(w, 201, map[string]any{"Id": "exec-" + c.ID[:12]})
	case method == "DELETE" && op == "":
		delete(fe.containers, c.ID)
		w.WriteHeader(204)
	default:
		jsonReply(w, 405, apiMsg("method not allowed"))
	}
}

func (fe *fakeEngine) networkOp(w http.ResponseWriter, method, rest string, body map[string]any) {
	name, op, _ := strings.Cut(rest, "/")
	n := fe.networks[name]
	if n == nil {
		jsonReply(w, 404, apiMsg("network "+name+" not found"))
		return
	}
	switch {
	case method == "GET" && op == "":
		jsonReply(w, 200, map[string]any{"Id": n.ID, "Name": name, "Driver": n.Driver, "Ingress": n.Ingress, "Attachable": n.Attachable})
	case method == "POST" && op == "connect":
		id, _ := body["Container"].(string)
		c := fe.findContainer(id)
		if c == nil {
			jsonReply(w, 404, apiMsg("No such container: "+id))
			return
		}
		c.Networks[name] = &fakeEndpoint{Aliases: anyStrings(anyMap(body["EndpointConfig"])["Aliases"])}
		w.WriteHeader(200)
	default:
		jsonReply(w, 405, apiMsg("method not allowed"))
	}
}

func (fe *fakeEngine) listServices(w http.ResponseWriter, q url.Values) {
	f := parseFilters(q.Get("filters"))
	out := []map[string]any{}
	for _, s := range fe.sortedServices() {
		if !labelsMatch(specLabels(s.Spec), f["label"]) {
			continue
		}
		out = append(out, s.view())
	}
	jsonReply(w, 200, out)
}

func (fe *fakeEngine) createService(w http.ResponseWriter, body map[string]any) {
	name, _ := body["Name"].(string)
	if fe.findService(name) != nil {
		jsonReply(w, 409, apiMsg("rpc error: code = AlreadyExists desc = name conflicts with an existing object"))
		return
	}
	s := &fakeService{ID: fe.serviceID(), Version: 1, Spec: roundTrip(body)}
	fe.services[s.ID] = s
	jsonReply(w, 201, map[string]any{"ID": s.ID})
}

func (fe *fakeEngine) serviceOp(w http.ResponseWriter, method, rest string, q url.Values, body map[string]any) {
	id, op, _ := strings.Cut(rest, "/")
	s := fe.findService(id)
	if s == nil {
		jsonReply(w, 404, apiMsg("service "+id+" not found"))
		return
	}
	switch {
	case method == "GET" && op == "":
		jsonReply(w, 200, s.view())
	case method == "DELETE" && op == "":
		delete(fe.services, s.ID)
		w.WriteHeader(200)
	case method == "POST" && op == "update":
		// The version is the concurrency guard the agent relies on: an update at
		// any other version is what Swarm refuses, and what a test that races
		// two writers would see.
		if v, err := strconv.ParseUint(q.Get("version"), 10, 64); err != nil || v != s.Version {
			jsonReply(w, 500, apiMsg("rpc error: code = Unknown desc = update out of sequence"))
			return
		}
		s.Spec = roundTrip(body)
		s.Version++
		jsonReply(w, 200, map[string]any{"Warnings": nil})
	default:
		jsonReply(w, 405, apiMsg("method not allowed"))
	}
}

func (fe *fakeEngine) listTasks(w http.ResponseWriter, q url.Values) {
	f := parseFilters(q.Get("filters"))
	out := []map[string]any{}
	for _, t := range fe.tasks {
		sid, _ := t["ServiceID"].(string)
		if len(f["service"]) > 0 {
			match := false
			for _, want := range f["service"] {
				if s := fe.findService(want); s != nil && s.ID == sid {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if ds, _ := t["DesiredState"].(string); len(f["desired-state"]) > 0 && !oneOf(f["desired-state"], ds) {
			continue
		}
		out = append(out, t)
	}
	jsonReply(w, 200, out)
}

func (fe *fakeEngine) listSecrets(w http.ResponseWriter, q url.Values) {
	f := parseFilters(q.Get("filters"))
	out := []map[string]any{}
	for _, s := range fe.sortedSecrets() {
		if labelsMatch(specLabels(s.Spec), f["label"]) {
			out = append(out, map[string]any{"ID": s.ID, "Spec": s.Spec})
		}
	}
	jsonReply(w, 200, out)
}

func (fe *fakeEngine) createSecret(w http.ResponseWriter, body map[string]any) {
	name, _ := body["Name"].(string)
	if fe.findSecret(name) != nil {
		jsonReply(w, 409, apiMsg("rpc error: code = AlreadyExists desc = secret "+name+" already exists"))
		return
	}
	s := &fakeSecret{ID: fe.serviceID(), Spec: roundTrip(body)}
	fe.secrets[s.ID] = s
	jsonReply(w, 201, map[string]any{"ID": s.ID})
}

func (fe *fakeEngine) secretOp(w http.ResponseWriter, method, id string) {
	s := fe.findSecret(id)
	if s == nil {
		jsonReply(w, 404, apiMsg("secret "+id+" not found"))
		return
	}
	if method != "DELETE" {
		jsonReply(w, 405, apiMsg("method not allowed"))
		return
	}
	delete(fe.secrets, s.ID)
	w.WriteHeader(204)
}

// ---- lookups (callers hold mu) ----

func (fe *fakeEngine) findContainer(idOrName string) *fakeContainer {
	for _, c := range fe.containers {
		if c.ID == idOrName || c.Name == idOrName || len(idOrName) >= 12 && strings.HasPrefix(c.ID, idOrName) {
			return c
		}
	}
	return nil
}

func (fe *fakeEngine) findService(idOrName string) *fakeService {
	for _, s := range fe.services {
		if s.ID == idOrName || specName(s.Spec) == idOrName {
			return s
		}
	}
	return nil
}

func (fe *fakeEngine) findSecret(idOrName string) *fakeSecret {
	for _, s := range fe.secrets {
		if s.ID == idOrName || specName(s.Spec) == idOrName {
			return s
		}
	}
	return nil
}

func (fe *fakeEngine) sortedContainers() []*fakeContainer {
	var out []*fakeContainer
	for _, c := range fe.containers {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (fe *fakeEngine) sortedServices() []*fakeService {
	var out []*fakeService
	for _, s := range fe.services {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (fe *fakeEngine) sortedSecrets() []*fakeSecret {
	var out []*fakeSecret
	for _, s := range fe.secrets {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (fe *fakeEngine) hexID() string {
	fe.seq++
	return fmt.Sprintf("%064x", fe.seq)
}

func (fe *fakeEngine) serviceID() string {
	fe.seq++
	return fmt.Sprintf("s%024d", fe.seq)
}

func (c *fakeContainer) state() string {
	if c.Running {
		return "running"
	}
	return "exited"
}

func (s *fakeService) view() map[string]any {
	return map[string]any{"ID": s.ID, "Version": map[string]any{"Index": s.Version}, "Spec": s.Spec}
}

// ---- seeding and reading, from the test (take mu) ----

func (fe *fakeEngine) addNetwork(name, driver string, attachable bool) *fakeNetwork {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	n := &fakeNetwork{ID: fe.serviceID(), Driver: driver, Attachable: attachable}
	fe.networks[name] = n
	return n
}

// addContainer seeds a container; nets maps each network it is on to its
// aliases there, and the address it holds is left to addAddress.
func (fe *fakeEngine) addContainer(name string, running bool, labels map[string]string, nets map[string][]string) *fakeContainer {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	c := &fakeContainer{ID: fe.hexID(), Name: name, Image: "example/workload:1", Running: running,
		Labels: map[string]string{}, Networks: map[string]*fakeEndpoint{}}
	for k, v := range labels {
		c.Labels[k] = v
	}
	for n, aliases := range nets {
		c.Networks[n] = &fakeEndpoint{Aliases: aliases}
	}
	fe.containers[c.ID] = c
	return c
}

// addService seeds a Swarm service in the shape the agent reads: name, labels,
// a replica count, and the networks (BY ID, as the daemon reports Targets) it
// is attached to with its aliases there.
func (fe *fakeEngine) addService(name string, labels map[string]string, replicas int, nets map[string][]string) *fakeService {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	var attach []map[string]any
	for n, aliases := range nets {
		target := n
		if net, ok := fe.networks[n]; ok {
			target = net.ID
		}
		attach = append(attach, map[string]any{"Target": target, "Aliases": aliases})
	}
	spec := map[string]any{
		"Name":   name,
		"Labels": labels,
		"Mode":   map[string]any{"Replicated": map[string]any{"Replicas": replicas}},
		"TaskTemplate": map[string]any{
			"ContainerSpec": map[string]any{"Image": "example/workload:1"},
			"Networks":      attach,
		},
	}
	s := &fakeService{ID: fe.serviceID(), Version: 1, Spec: roundTrip(spec)}
	fe.services[s.ID] = s
	return s
}

func (fe *fakeEngine) addTask(serviceID, node, state, containerID string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.tasks = append(fe.tasks, map[string]any{
		"ID": fe.serviceID(), "ServiceID": serviceID, "NodeID": node, "DesiredState": "running",
		"CreatedAt": time.Now().UTC().Format(time.RFC3339Nano),
		"Status":    map[string]any{"State": state, "ContainerStatus": map[string]any{"ContainerID": containerID}},
	})
}

func (fe *fakeEngine) addSecret(name string, labels map[string]string) *fakeSecret {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	s := &fakeSecret{ID: fe.serviceID(), Spec: roundTrip(map[string]any{"Name": name, "Labels": labels})}
	fe.secrets[s.ID] = s
	return s
}

// setContainerMounts gives a seeded container the mounts inspect reports.
func (fe *fakeEngine) setContainerMounts(id string, mounts ...dockerMount) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if c := fe.findContainer(id); c != nil {
		c.Mounts = mounts
	}
}

// setServiceMounts gives a seeded service the mounts its ContainerSpec
// declares, in the shape the daemon reports them.
func (fe *fakeEngine) setServiceMounts(id string, mounts ...dockerMount) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	s := fe.findService(id)
	if s == nil {
		return
	}
	var out []any
	for _, m := range mounts {
		out = append(out, map[string]any{"Type": m.Type, "Source": m.Source, "Target": m.Destination})
	}
	anyMap(anyMap(s.Spec["TaskTemplate"])["ContainerSpec"])["Mounts"] = out
}

// setSwarm makes the fake a Swarm manager (or not): what /info reports and
// whether the service, secret and task endpoints answer at all.
func (fe *fakeEngine) setSwarm(manager bool) {
	fe.mu.Lock()
	fe.swarm = manager
	fe.mu.Unlock()
	fe.forgetSwarm()
}

// forgetSwarm makes the agent ask /info again: it remembers a manager probe
// that answered, and a test that turns Swarm on mid-way is a node promoted
// under a running agent, which the real one would only see at its next start.
func (fe *fakeEngine) forgetSwarm() {
	swarmControl = memoize(swarmControlProbe)
}

// refuseWith installs the failure hook: a non-zero code answers that request
// with that message instead of serving it. nil lifts it.
func (fe *fakeEngine) refuseWith(f func(method, path string) (int, string)) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.refuse = f
}

func (fe *fakeEngine) container(idOrName string) *fakeContainer {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	c := fe.findContainer(idOrName)
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

func (fe *fakeEngine) service(idOrName string) *fakeService {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	s := fe.findService(idOrName)
	if s == nil {
		return nil
	}
	return &fakeService{ID: s.ID, Version: s.Version, Spec: roundTrip(s.Spec)}
}

func (fe *fakeEngine) secret(idOrName string) *fakeSecret {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	s := fe.findSecret(idOrName)
	if s == nil {
		return nil
	}
	return &fakeSecret{ID: s.ID, Spec: roundTrip(s.Spec)}
}

// serviceSpec is a service's Spec decoded into the fields the tests assert on.
type serviceSpec struct {
	Name   string
	Labels map[string]string
	Mode   struct {
		Replicated *struct{ Replicas int }
	}
	TaskTemplate struct {
		ContainerSpec struct {
			Image   string
			Command []string
			Env     []string
			User    string
			Mounts  []map[string]any
			Secrets []struct {
				SecretName string
				File       struct{ Name string }
			}
		}
		Networks []struct {
			Target  string
			Aliases []string
		}
		Placement struct{ Constraints []string }
	}
	EndpointSpec struct{ Mode string }
}

func (fe *fakeEngine) spec(idOrName string) (serviceSpec, bool) {
	fe.t.Helper()
	var out serviceSpec
	s := fe.service(idOrName)
	if s == nil {
		return out, false
	}
	decodeInto(fe.t, s.Spec, &out)
	return out, true
}

// requests is the journal so far: one "METHOD /path?query" per request.
func (fe *fakeEngine) requests() []string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return append([]string(nil), fe.log...)
}

// ---- shared helpers (the k8s fake uses them too) ----

func jsonReply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiMsg(msg string) map[string]any { return map[string]any{"message": msg} }

// fakeDigest is a stable 64-hex "digest" for an image name: the fake needs an
// image id and a RepoDigest that look like the daemon's.
func fakeDigest(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])
}

func parseFilters(raw string) map[string][]string {
	f := map[string][]string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &f)
	}
	return f
}

// labelsMatch applies a Docker label filter: every "k=v" (or bare "k") wanted
// must be on the object.
func labelsMatch(labels map[string]string, wants []string) bool {
	for _, want := range wants {
		k, v, hasValue := strings.Cut(want, "=")
		got, present := labels[k]
		if !present || hasValue && got != v {
			return false
		}
	}
	return true
}

func specName(spec map[string]any) string {
	n, _ := spec["Name"].(string)
	return n
}

func specLabels(spec map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range anyMap(spec["Labels"]) {
		out[k], _ = v.(string)
	}
	return out
}

// roundTrip normalises a value through JSON, so a seeded map[string]string and
// a decoded map[string]any read the same way.
func roundTrip(v any) map[string]any {
	b, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func decodeInto(t *testing.T, v any, out any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decoding %s: %v", b, err)
	}
}

func anyMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func anyStrings(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, _ := e.(string)
			out = append(out, str)
		}
		return out
	}
	return nil
}

func oneOf(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// ---- the agent's own seams, for one test each ----

// agentIs hands the Docker backends an identity without a daemon to ask.
func agentIs(t *testing.T, self selfInfo) {
	t.Helper()
	old := dockerSelf
	dockerSelf = func() (selfInfo, error) { return self, nil }
	t.Cleanup(func() { dockerSelf = old })
}

// liveSessions replaces sessionLive for one test: only the addresses in the
// returned set answer, so no test ever dials a container name. The set is the
// test's to change mid-way (a session that dies).
func liveSessions(t *testing.T, addrs ...string) map[string]bool {
	t.Helper()
	live := map[string]bool{}
	for _, a := range addrs {
		live[a] = true
	}
	old := sessionLive
	sessionLive = func(owner string) bool { return live[owner] }
	t.Cleanup(func() { sessionLive = old })
	return live
}

// verbReply runs a verb body and returns the one line the agent would have
// printed, through the agent's own ending (captureVerb): answer and fatal end
// a verb with a panic the process entry point turns into the exit, and this is
// that entry point for a test, which then has control back to look at the
// cluster afterwards. A body that returns without answering is a failure of
// the body, said as such.
func verbReply(t *testing.T, fn func()) string {
	t.Helper()
	e, exited := captureVerb(fn)
	if !exited {
		t.Fatal("the verb returned without answering: a subprocess would have exited 0 and said nothing")
	}
	return e.reply
}

// stderrOf runs fn with os.Stderr captured: what the sweeps say (gcNote) goes
// there, and a test of "said once" has to count lines.
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	got := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		got <- string(b)
	}()
	func() {
		defer func() {
			os.Stderr = old
			w.Close()
		}()
		fn()
	}()
	return <-got
}

// resetGcNotes forgets what earlier sweeps said, so a test of the once-per-boot
// rule starts from a fresh boot.
func resetGcNotes() {
	gcNotedMu.Lock()
	gcNoted = map[string]bool{}
	gcNotedMu.Unlock()
}
