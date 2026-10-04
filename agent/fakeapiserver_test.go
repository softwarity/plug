package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake Kubernetes API server, in memory, behind an httptest.Server: the
// Docker fake's twin (fakeengine_test.go), for the same reason.
//
// It holds namespaced objects by resource (services, endpoints, endpointslices,
// pods, deployments, persistentvolumeclaims: whatever a test seeds) and answers
// GET, POST, PUT, PATCH (RFC 7386 merge, the one shape the agent sends),
// DELETE and DELETE on a collection, with the labelSelector and fieldSelector
// the agent uses. A created Service gets a ClusterIP and a uid, which is how a
// test can tell "taken over in place" from "deleted and recreated". Every call
// must carry the ServiceAccount token the test wrote, so the token seam is
// proven by every test and not only by one.
//
// Not covered: admission (a Service with no ports is accepted here), the
// endpoints and endpointslice controllers (nothing here writes an object the
// agent did not), watches, the exec subresource beyond a scripted stdout
// (execAnswers; a 404 for a missing pod, a 400 otherwise), and server-side
// validation of what a merge patch produces.

const fakeK8sToken = "unit-test-token"

type fakeAPIServer struct {
	t   *testing.T
	srv *httptest.Server
	ns  string

	mu      sync.Mutex
	seq     int
	objects map[string]map[string]map[string]any // resource -> name -> object
	log     []string
	refuse  func(method, path string) (int, string)
	execOut map[string]string // "cat /proc/1/status" -> what the exec subresource writes on stdout
}

// newFakeAPIServer starts the fake in namespace ns and points every seam the
// Kubernetes backend has at it: base URL, client, ServiceAccount files (token,
// namespace, CA) and this pod's address.
func newFakeAPIServer(t *testing.T, ns, podIP string) *fakeAPIServer {
	t.Helper()
	fa := &fakeAPIServer{t: t, ns: ns, objects: map[string]map[string]map[string]any{}}
	fa.srv = httptest.NewServer(fa)
	sa := t.TempDir()
	for name, body := range map[string]string{"token": fakeK8sToken + "\n", "namespace": ns, "ca.crt": ""} {
		if err := os.WriteFile(filepath.Join(sa, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldSA, oldBase, oldClient, oldSock, oldDial := k8sSA, k8sAPIBase, k8sClient, dockerSock, k8sExecDial
	k8sSA, k8sAPIBase = sa, fa.srv.URL
	k8sClient = func() *http.Client { return fa.srv.Client() }
	// The exec handshake dials for itself, over TLS; the fake speaks plain HTTP.
	k8sExecDial = func(addr, _ string) (net.Conn, error) { return net.DialTimeout("tcp", addr, 5*time.Second) }
	// No Docker socket, whatever the host has: the k8s backend must be the one
	// answering here.
	dockerSock = filepath.Join(t.TempDir(), "absent.sock")
	t.Setenv("PLUG_POD_IP", podIP)
	t.Cleanup(func() {
		fa.srv.Client().CloseIdleConnections()
		fa.srv.Close()
		k8sSA, k8sAPIBase, k8sClient, dockerSock, k8sExecDial = oldSA, oldBase, oldClient, oldSock, oldDial
	})
	return fa
}

func (fa *fakeAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.log = append(fa.log, r.Method+" "+r.URL.RequestURI())
	if r.Header.Get("Authorization") != "Bearer "+fakeK8sToken {
		k8sStatus(w, 401, "Unauthorized")
		return
	}
	if fa.refuse != nil {
		if code, msg := fa.refuse(r.Method, r.URL.Path); code != 0 {
			k8sStatus(w, code, msg)
			return
		}
	}
	res, name, sub, ok := fa.route(r.URL.Path)
	if !ok {
		k8sStatus(w, 404, "the server could not find the requested resource")
		return
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	q := r.URL.Query()
	coll := fa.collection(res)
	switch {
	case sub != "":
		// pods/<name>/exec and the like: enough to tell a missing pod from one
		// that exists, which is all the RBAC probes read.
		out, scripted := fa.execOut[strings.Join(q["command"], " ")]
		switch {
		case coll[name] == nil:
			k8sStatus(w, 404, fmt.Sprintf("pods %q not found", name))
		case sub == "exec" && scripted && r.Header.Get("Upgrade") == "websocket":
			execReply(w, out)
		default:
			k8sStatus(w, 400, "Upgrade request required")
		}
	case name == "" && r.Method == "GET":
		items := []map[string]any{}
		for _, n := range sortedKeys(coll) {
			if selectorsMatch(coll[n], q.Get("labelSelector"), q.Get("fieldSelector")) {
				items = append(items, coll[n])
			}
		}
		jsonReply(w, 200, map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	case name == "" && r.Method == "POST":
		n, _ := anyMap(body["metadata"])["name"].(string)
		if n == "" {
			k8sStatus(w, 422, "metadata.name: Required value")
			return
		}
		if coll[n] != nil {
			k8sStatus(w, 409, fmt.Sprintf("%s %q already exists", res, n))
			return
		}
		fa.stamp(res, body)
		coll[n] = body
		jsonReply(w, 201, body)
	case name == "" && r.Method == "DELETE":
		for _, n := range sortedKeys(coll) {
			if selectorsMatch(coll[n], q.Get("labelSelector"), q.Get("fieldSelector")) {
				delete(coll, n)
			}
		}
		jsonReply(w, 200, map[string]any{"kind": "Status", "status": "Success"})
	case coll[name] == nil:
		k8sStatus(w, 404, fmt.Sprintf("%s %q not found", res, name))
	case r.Method == "GET":
		jsonReply(w, 200, coll[name])
	case r.Method == "PUT":
		// A replace keeps what the server owns: the uid and the ClusterIP.
		old := coll[name]
		fa.stamp(res, body)
		anyMap(body["metadata"])["uid"] = anyMap(old["metadata"])["uid"]
		if res == "services" {
			anyMap(body["spec"])["clusterIP"] = anyMap(old["spec"])["clusterIP"]
		}
		coll[name] = body
		jsonReply(w, 200, body)
	case r.Method == "PATCH":
		if r.Header.Get("Content-Type") != "application/merge-patch+json" {
			k8sStatus(w, 415, "the body of the request was in an unknown format - accepted media types include: application/merge-patch+json")
			return
		}
		coll[name] = anyMap(mergePatch(coll[name], body))
		jsonReply(w, 200, coll[name])
	case r.Method == "DELETE":
		delete(coll, name)
		jsonReply(w, 200, map[string]any{"kind": "Status", "status": "Success"})
	default:
		k8sStatus(w, 405, "method not allowed")
	}
}

// route reads "<prefix>/namespaces/<ns>/<resource>[/<name>[/<subresource>]]"
// for the core group (/api/v1) and any named group (/apis/<group>/<version>).
func (fa *fakeAPIServer) route(path string) (res, name, sub string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var rest []string
	switch {
	case len(parts) >= 2 && parts[0] == "api" && parts[1] == "v1":
		rest = parts[2:]
	case len(parts) >= 3 && parts[0] == "apis":
		rest = parts[3:]
	default:
		return "", "", "", false
	}
	if len(rest) < 3 || rest[0] != "namespaces" || rest[1] != fa.ns {
		return "", "", "", false
	}
	res = rest[2]
	if len(rest) > 3 {
		name = rest[3]
	}
	if len(rest) > 4 {
		sub = rest[4]
	}
	return res, name, sub, true
}

func (fa *fakeAPIServer) collection(res string) map[string]map[string]any {
	if fa.objects[res] == nil {
		fa.objects[res] = map[string]map[string]any{}
	}
	return fa.objects[res]
}

// stamp is what the server adds to an object it stores: a namespace, a uid,
// for a Service that asked for none a ClusterIP, and for a pod with no status
// yet what the scheduler and the kubelet would give it, an address and a
// phase, since the mount waits for the address before it answers.
func (fa *fakeAPIServer) stamp(res string, obj map[string]any) {
	meta := anyMap(obj["metadata"])
	if meta == nil {
		meta = map[string]any{}
		obj["metadata"] = meta
	}
	fa.seq++
	meta["namespace"] = fa.ns
	meta["uid"] = fmt.Sprintf("uid-%d", fa.seq)
	if res == "pods" && obj["status"] == nil {
		obj["status"] = map[string]any{"phase": "Running", "podIP": fmt.Sprintf("10.244.2.%d", fa.seq)}
	}
	if res != "services" {
		return
	}
	spec := anyMap(obj["spec"])
	if spec == nil {
		spec = map[string]any{}
		obj["spec"] = spec
	}
	if ip, _ := spec["clusterIP"].(string); ip == "" && spec["type"] != "ExternalName" {
		spec["clusterIP"] = fmt.Sprintf("10.96.0.%d", fa.seq)
	}
}

// mergePatch is RFC 7386: maps merge key by key, null deletes a key, anything
// else replaces whole. It is what the agent relies on to drop a selector
// (`selector: null`) and to clear one annotation among others.
func mergePatch(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergePatch(tm[k], v)
	}
	return tm
}

// selectorsMatch applies a labelSelector ("k=v,k2!=v2,k3") and a fieldSelector
// (metadata.name=, metadata.namespace=) the way the agent writes them.
func selectorsMatch(obj map[string]any, labelSel, fieldSel string) bool {
	meta := anyMap(obj["metadata"])
	labels := anyMap(meta["labels"])
	for _, req := range splitSelector(labelSel) {
		switch {
		case strings.Contains(req, "!="):
			k, v, _ := strings.Cut(req, "!=")
			if got, _ := labels[k].(string); got == v {
				return false
			}
		case strings.Contains(req, "="):
			k, v, _ := strings.Cut(strings.ReplaceAll(req, "==", "="), "=")
			if got, _ := labels[k].(string); got != v {
				return false
			}
		default:
			if _, present := labels[req]; !present {
				return false
			}
		}
	}
	for _, req := range splitSelector(fieldSel) {
		k, v, _ := strings.Cut(req, "=")
		field := strings.TrimPrefix(k, "metadata.")
		if got, _ := meta[field].(string); got != v {
			return false
		}
	}
	return true
}

func splitSelector(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func k8sStatus(w http.ResponseWriter, code int, msg string) {
	jsonReply(w, code, map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "message": msg, "code": code})
}

func sortedKeys(m map[string]map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- seeding and reading, from the test ----

// add stores an object as if it had been created: stamped with a uid and, for
// a Service, a ClusterIP unless the test chose one.
func (fa *fakeAPIServer) add(res string, obj map[string]any) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	o := roundTrip(obj)
	fa.stamp(res, o)
	name, _ := anyMap(o["metadata"])["name"].(string)
	fa.collection(res)[name] = o
}

// get returns a copy of an object, or nil.
func (fa *fakeAPIServer) get(res, name string) map[string]any {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	o := fa.collection(res)[name]
	if o == nil {
		return nil
	}
	return roundTrip(o)
}

// edit changes a stored object in place, from the test: a linger stamp moved
// into the past, say.
func (fa *fakeAPIServer) edit(res, name string, fn func(obj map[string]any)) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if o := fa.collection(res)[name]; o != nil {
		fn(o)
	}
}

// execAnswers scripts the exec subresource: a command (argv joined by spaces)
// and what it writes on stdout, for every pod. Unscripted, exec stays a 400.
func (fa *fakeAPIServer) execAnswers(command, stdout string) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.execOut == nil {
		fa.execOut = map[string]string{}
	}
	fa.execOut[command] = stdout
}

// execReply is the exec subresource's session, the smallest one the agent
// reads (k8sExec): the WebSocket upgrade, stdout on channel 1, the API
// server's verdict on channel 3, a close frame.
func execReply(w http.ResponseWriter, stdout string) {
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	frame := func(opcode byte, payload []byte) {
		buf.WriteByte(0x80 | opcode)
		if len(payload) < 126 {
			buf.WriteByte(byte(len(payload)))
		} else {
			buf.Write([]byte{126, byte(len(payload) >> 8), byte(len(payload))})
		}
		buf.Write(payload)
	}
	buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	frame(0x2, append([]byte{1}, stdout...))
	frame(0x2, append([]byte{3}, `{"status":"Success"}`...))
	frame(0x8, nil)
	_ = buf.Flush()
}

func (fa *fakeAPIServer) refuseWith(f func(method, path string) (int, string)) {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	fa.refuse = f
}

func (fa *fakeAPIServer) requests() []string {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]string(nil), fa.log...)
}

// svcView is a Service decoded into what the tests assert on.
type svcView struct {
	Metadata struct {
		Name        string
		UID         string
		Labels      map[string]string
		Annotations map[string]string
	}
	Spec struct {
		Selector  map[string]string
		ClusterIP string
		Type      string
		Ports     []struct {
			Name       string
			Port       int
			TargetPort any
		}
	}
}

func (fa *fakeAPIServer) service(name string) (svcView, bool) {
	fa.t.Helper()
	var v svcView
	o := fa.get("services", name)
	if o == nil {
		return v, false
	}
	decodeInto(fa.t, o, &v)
	return v, true
}

// endpointsView is an Endpoints object decoded into what the tests read.
type endpointsView struct {
	Subsets []struct {
		Addresses []struct{ IP string }
		Ports     []struct {
			Name string
			Port int
		}
	}
}

func (fa *fakeAPIServer) endpoints(name string) (endpointsView, bool) {
	fa.t.Helper()
	var v endpointsView
	o := fa.get("endpoints", name)
	if o == nil {
		return v, false
	}
	decodeInto(fa.t, o, &v)
	return v, true
}

// k8sObject builds a namespaced object for seeding.
func k8sObject(kind, name string, labels, annotations map[string]string, spec map[string]any) map[string]any {
	meta := map[string]any{"name": name}
	if labels != nil {
		meta["labels"] = labels
	}
	if annotations != nil {
		meta["annotations"] = annotations
	}
	return map[string]any{"apiVersion": "v1", "kind": kind, "metadata": meta, "spec": spec}
}
