package agent

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Everything the agent does through the Kubernetes API.
//
// Moved out of main.go, which held all three orchestrators at three thousand
// lines: twenty-six functions for this one, twelve for Docker and five for
// Swarm, interleaved. Nothing here changed, and nothing could: moving a function
// between files of the same package is invisible to the compiler and to the
// linker. What it buys is that reading how a Service is repointed no longer
// means scrolling through how a container is parked.

// k8sAgentDeployment finds the agent's own Deployment (label app=plug, this
// namespace) and the container running the plug image — a pod may carry
// sidecars, and patching the wrong one would be silent.
func k8sAgentDeployment() (depName, container, img string, code int, err error) {
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
	code, err = k8sAPI("GET", "/apis/apps/v1/namespaces/"+k8sNamespace()+"/deployments?labelSelector=app%3Dplug", nil, &list)
	if err != nil || len(list.Items) == 0 {
		return "", "", "", code, err
	}
	dep := list.Items[0]
	for _, c := range dep.Spec.Template.Spec.Containers {
		if strings.Contains(c.Image, "plug") {
			return dep.Metadata.Name, c.Name, c.Image, code, nil
		}
	}
	if len(dep.Spec.Template.Spec.Containers) == 1 {
		c := dep.Spec.Template.Spec.Containers[0]
		return dep.Metadata.Name, c.Name, c.Image, code, nil
	}
	return dep.Metadata.Name, "", "", code, nil
}

// k8sSelfUpdate updates the agent's own Deployment. A pinned RELEASE tag is
// rewritten to the newest release — a rolling restart alone would re-pull the
// same pin forever. A moving tag keeps the restart-only path (the annotation
// patch `kubectl rollout restart` uses), which makes the node re-pull it per
// its imagePullPolicy — Always in the official manifest.
func k8sSelfUpdate(decide func(string) (string, string, string)) {
	ns := k8sNamespace()
	name, container, img, code, err := k8sAgentDeployment()
	if err != nil {
		if code == 403 {
			answer("error: the deployed RBAC predates self-update — re-apply deploy/plug-k8s.yaml (it adds the deployments grant), or run: kubectl -n %s rollout restart deployment plug", ns)
		}
		answer("error: finding the agent deployment: %v", err)
	}
	if name == "" {
		answer("error: no deployment labeled app=plug in namespace %s — restart the agent's workload by hand", ns)
	}

	target, plan, note := decide(img)
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

// k8sTargetPort reads the targetPort out of a plug Service's ports.
func k8sTargetPort(raw json.RawMessage) string {
	var ports []struct {
		TargetPort any `json:"targetPort"`
	}
	if json.Unmarshal(raw, &ports) != nil || len(ports) == 0 {
		return ""
	}
	switch v := ports[0].TargetPort.(type) {
	case float64:
		return strconv.Itoa(int(v))
	case string:
		return v
	}
	return ""
}

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
	return readAPIReply(resp.StatusCode, resp.Status, data, out, false)
}

func k8sNamespace() string {
	ns, _ := os.ReadFile(k8sSA + "/namespace")
	return strings.TrimSpace(string(ns))
}

// k8sSignReceipt records WHO is parking, without disturbing WHAT was parked. A
// second session taking the same Service over inherits the duty of restoring it,
// so the owner is refreshed while the saved selector and ports stay exactly as
// they are: only the FIRST takeover ever saw the original, and re-saving would
// hand the way back to a Service that already points at an agent.
func k8sSignReceipt(raw, owner string, fresh k8sReceipt) (string, error) {
	r := fresh
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			// Unreadable: leave the only way back untouched rather than rewrite
			// it. The restore says the same thing when it gets there.
			return raw, nil
		}
	}
	r.Owner = owner
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// k8sReceiptOwner is the owner an annotation names, "" when there is no receipt,
// no owner, or nothing readable.
func k8sReceiptOwner(raw string) string {
	var r k8sReceipt
	if raw == "" || json.Unmarshal([]byte(raw), &r) != nil {
		return ""
	}
	return r.Owner
}

// k8sRepointPatch builds the merge patch that points an EXISTING Service at the
// agent — the ONE place both branches (a real workload taken over, and a plug
// Service reclaimed from a linger or a crash) build it, because they had drifted
// apart and only one of them was right.
//
// `selector: null` drops the selector outright, and that is the point: a selector
// names a ROLE (`app: plug` matches every replica of the agent) while the session
// serving the name lives in exactly ONE pod. At more than one replica the name
// then works for one request in N, which is not a slow name but a lottery. Without
// a selector the endpoints controller keeps its hands off and the Endpoints are
// ours to write (k8sPointAtSelf), naming the POD that holds the session.
//
// It also retires a whole class of failure this patch had to defend against. The
// reclaim branch used to write a bare {app: plug}, and since RFC 7386 MERGES maps,
// on a Service whose selector carried more than that key the result demanded
// app=plug AND app.kubernetes.io/name=<the workload>: no pod at all, zero
// endpoints, and a name that times out. Deleting the key cannot half-merge.
func k8sRepointPatch(pairs []portPair, ann map[string]any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"annotations": ann},
		"spec": map[string]any{
			"selector": nil,
			"ports":    k8sPorts(pairs),
		},
	}
}

// k8sSelectorFallback puts the OLD shape back: a Service selecting the agent by
// label. It is what a cluster whose deployed RBAC predates the endpoints grant
// gets, and it is right there: that RBAC was written for the single replica the
// manifest deploys, where `app: plug` and "this pod" are the same pod.
//
// Answering an error instead would break a working deployment on nothing but an
// image upgrade: `plug update` moves the image and never the manifest, so an
// agent that self-updated would stop serving names until someone re-applied a
// YAML file. The gap is named at boot instead (k8sNoteEndpointsGrant), where a
// line on stderr is the container's log rather than the one line of protocol a
// verb is allowed to print.
func k8sSelectorFallback(ns, name string) error {
	patch := map[string]any{"spec": map[string]any{"selector": map[string]string{"app": "plug"}}}
	_, err := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch)
	return err
}

// k8sUnroutable explains why an EXISTING Service cannot carry a plug name, or
// returns "" when it can. Two shapes cannot, and NEITHER fails at patch time:
//
//   - headless (clusterIP: None): there is no virtual IP, so the name resolves
//     straight to pod IPs and targetPort is never applied. A caller would reach
//     the agent pod on the CLUSTER port — where nothing listens, the session's
//     forward sitting on a port sshd allocated.
//   - type: ExternalName: a DNS alias elsewhere, carrying no endpoints and no
//     ports at all.
//
// Both patch cleanly and then time out, which is exactly how one real session
// spent 90s and 95 attempts blaming the cluster's scheduler. clusterIP is
// immutable, so there is no in-place fix to suggest: the Service goes, or the
// name does.
func k8sUnroutable(name, typ, clusterIP string) string {
	switch {
	case clusterIP == "None":
		return fmt.Sprintf("the Service %q is headless (clusterIP: None) — the name resolves straight to pod IPs "+
			"and targetPort is never applied, so plug cannot route a session through it. Delete it and let plug "+
			"create the name (kubectl delete service %s), or serve a different name", name, name)
	case typ == "ExternalName":
		return fmt.Sprintf("the Service %q is a type: ExternalName — a DNS alias carrying no endpoints and no ports, "+
			"so plug cannot route a session through it. Delete it and let plug create the name "+
			"(kubectl delete service %s), or serve a different name", name, name)
	}
	return ""
}

// k8sPorts renders the pairs as a Service's ports. The per-port name is
// REQUIRED by k8s as soon as there is more than one — a multi-port service
// (HTTP+SMTP+POP3 on one name) is exactly the case this serves.
func k8sPorts(pairs []portPair) []map[string]any {
	out := make([]map[string]any, 0, len(pairs))
	for _, pp := range pairs {
		c, _ := strconv.Atoi(pp.cluster)
		a, _ := strconv.Atoi(pp.agent)
		out = append(out, map[string]any{"name": k8sPortName(pp), "port": c, "targetPort": a})
	}
	return out
}

// k8sPortName is that name, in one place because the Endpoints must repeat it
// verbatim: k8s matches a Service port to an endpoint port BY NAME, and a
// mismatch is a name with no route rather than a rejected object.
func k8sPortName(pp portPair) string { return "p" + pp.cluster }

// k8sSelfIP is THIS pod's address: what a served name must resolve to, and half
// of the identity its parking receipt is signed with.
//
// The manifest supplies it through the downward API, which is the only source
// that is certain. The fallback matters as much, because an agent linked into a
// gateway is deployed by whoever wrote THAT manifest and cannot demand a line in
// it: the source address the kernel would pick to reach the API server is this
// pod's address on the cluster network. `net.Dial` on UDP only binds a socket
// and sends nothing, and it needs no RBAC. KUBERNETES_SERVICE_HOST is an address
// the kubelet always sets, so the fallback does not depend on DNS either.
func k8sSelfIP() string {
	if ip := os.Getenv("PLUG_POD_IP"); ip != "" {
		return ip
	}
	api := firstNonEmpty(os.Getenv("KUBERNETES_SERVICE_HOST"), "kubernetes.default.svc")
	c, err := net.Dial("udp", net.JoinHostPort(api, "443"))
	if err != nil {
		return ""
	}
	defer c.Close()
	host, _, err := net.SplitHostPort(c.LocalAddr().String())
	if err != nil {
		return ""
	}
	return host
}

// k8sServiceFor is the Service a served name IS: no selector, and an annotation
// naming the pod that holds the session.
//
// A selector would name a role. `app: plug` matches every replica of the agent,
// while the forward carrying the name lives in exactly one pod, so the name would
// work for one request in N - not degraded, drawn by lot. What replaces it is
// k8sEndpointsFor, which names an address.
func k8sServiceFor(name, owner string, pairs []portPair) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":   name,
			"labels": map[string]string{k8sManaged: "plug"},
			// The label says a plug Service; this says WHOSE, so a sibling
			// replica's boot gc can tell a live session's name from a crashed
			// session's leftover instead of sweeping both alike.
			"annotations": map[string]string{sessionOwnerLabel: owner},
		},
		"spec": map[string]any{"ports": k8sPorts(pairs)},
	}
}

// k8sEndpointsFor is what makes a selector-less Service reach exactly this pod:
// one address, and one port per exposure carrying the port the session's forward
// actually listens on. targetPort is not consulted for a Service without a
// selector, so the endpoint's own port is the whole routing decision.
//
// Deliberately unlabelled. The Service says whose the name is; an Endpoints
// object that outlives a takeover is simply adopted by the endpoints controller
// the moment the selector comes back, and a plug label on it would be one more
// thing to clean up for no one to read.
func k8sEndpointsFor(name, podIP string, pairs []portPair) map[string]any {
	ports := make([]map[string]any, 0, len(pairs))
	for _, pp := range pairs {
		a, _ := strconv.Atoi(pp.agent)
		ports = append(ports, map[string]any{"name": k8sPortName(pp), "port": a})
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Endpoints",
		"metadata":   map[string]any{"name": name},
		"subsets": []map[string]any{{
			"addresses": []map[string]any{{"ip": podIP}},
			"ports":     ports,
		}},
	}
}

// k8sWriteEndpoints creates them, or replaces whatever is there: a previous
// session's, or the ones the endpoints controller wrote while the Service still
// had a selector. PUT rather than PATCH so no subset of a stale object survives.
func k8sWriteEndpoints(ns, name, podIP string, pairs []portPair) (int, error) {
	ep := k8sEndpointsFor(name, podIP, pairs)
	code, err := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/endpoints", ep, nil)
	if code == 409 {
		return k8sAPI("PUT", "/api/v1/namespaces/"+ns+"/endpoints/"+name, ep, nil)
	}
	return code, err
}

// k8sPointAtSelf finishes a serve: the name now exists, and this is what makes
// it reach THIS pod. Only two things send it to the old selector shape - no
// address to publish, and no grant to publish it with - and both are a
// deployment that predates this, never a cluster refusing the write. A real
// failure stops the serve, because a name that resolves to nothing is the one
// outcome worse than no name.
func k8sPointAtSelf(ns, name, podIP string, pairs []portPair) {
	if podIP != "" {
		code, err := k8sWriteEndpoints(ns, name, podIP, pairs)
		switch endpointsOutcome(code, err) {
		case endpointsDone:
			return
		case endpointsFatal:
			answer("error: pointing %q at this agent (writing its endpoints): %v", name, err)
		}
	}
	if err := k8sSelectorFallback(ns, name); err != nil {
		answer("error: pointing %q at this agent: %v", name, err)
	}
}

// k8sDropName removes a plug-created name whole. The endpoints controller sweeps
// an Endpoints object whose Service is gone, but only once it notices; deleting
// both here means the name never survives its Service even for a moment. Safe
// because it is only ever called for a Service labelled plug-managed, whose
// Endpoints of the same name are this agent's own.
func k8sDropName(ns, name string) {
	_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil)
	_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/endpoints/"+name, nil, nil)
}

// k8sNoteEndpointsGrant says, once per boot and in the container's log, that this
// agent will fall back to the old selector shape. A verb cannot say it: its
// stdout and stderr are merged into the one line the CLI reads as the answer, so
// a warning there would BE the answer. Boot is the other moment the agent runs
// code, and it is where the person who applied the manifest is looking.
func k8sNoteEndpointsGrant(ns string) {
	if k8sSelfIP() == "" {
		gcNote("this pod's address is unknown, so a served name will select every agent replica by " +
			"label instead of naming this pod - right at one replica, a lottery past it. Re-apply " +
			"deploy/plug-k8s.yaml, or set PLUG_POD_IP from the downward API (status.podIP)")
		return
	}
	// A GET on a name nothing uses separates "may not touch endpoints" (403) from
	// "there is none" (404): one call, and no object created to find out.
	if code, _ := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/endpoints/plug-endpoints-grant-probe", nil, nil); code == 403 {
		gcNote("the deployed RBAC predates the endpoints grant, so a served name will select every agent " +
			"replica by label instead of naming this pod - right at one replica, a lottery past it. " +
			"Re-apply deploy/plug-k8s.yaml")
	}
}

func k8sServe(name string, pairs []portPair) {
	ns := k8sNamespace()
	sweepExpiredK8sLingers(ns)
	podIP := k8sSelfIP()
	owner := sessionOwner(podIP, pairs)
	svc := k8sServiceFor(name, owner, pairs)
	code, err := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil)
	switch {
	case err == nil:
		k8sPointAtSelf(ns, name, podIP, pairs)
		answer("dynamic")
	case code == 403:
		// No RBAC → the opt-in was never applied, so it cannot create the name.
		answer("error: the agent may not manage Services in namespace %s — apply the RBAC (deploy/plug-k8s.yaml)", ns)
	case code == 409:
		// The name exists. A previous plug session's leftover is replaced; a REAL
		// Service keeps its name — unless takeover, which repoints it at the agent
		// for the session (its endpoints and ports), receipt in an annotation on
		// itself.
		var existing struct {
			Metadata struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Spec struct {
				Selector  map[string]string `json:"selector"`
				Ports     json.RawMessage   `json:"ports"`
				Type      string            `json:"type"`
				ClusterIP string            `json:"clusterIP"`
			} `json:"spec"`
		}
		_, gerr := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing)
		// A Service plug cannot route THROUGH is refused here, in the millisecond
		// the agent already holds the object — not 90 seconds later as a timeout
		// the caller has to interpret. Repointing one SUCCEEDS (selector and ports
		// patch cleanly) and yields a name that answers nobody: the worst of both,
		// since the deployed workload is parked and the replacement never carries
		// traffic. Checked before the ownership split, so it covers a takeover and
		// a stale plug Service alike.
		if gerr == nil {
			if why := k8sUnroutable(name, existing.Spec.Type, existing.Spec.ClusterIP); why != "" {
				answer("error: %s", why)
			}
		}
		if gerr != nil || existing.Metadata.Labels[k8sManaged] != "plug" {
			if gerr == nil {
				// A REAL Service, and it may already be parked by a session that
				// is still serving it from another pod: taking it over now would
				// leave that session's name pointing here, and the workload parked
				// twice over. The receipt says whose it is; the address says
				// whether that is still true.
				if own := k8sReceiptOwner(existing.Metadata.Annotations[k8sParkedAnn]); own != owner && sessionLive(own) {
					answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
				}
				receipt, rerr := k8sSignReceipt(existing.Metadata.Annotations[k8sParkedAnn], owner,
					k8sReceipt{Selector: existing.Spec.Selector, Ports: existing.Spec.Ports})
				if rerr != nil {
					answer("error: recording %q's original spec: %v", name, rerr)
				}
				patch := k8sRepointPatch(pairs, map[string]any{k8sParkedAnn: receipt, sessionOwnerLabel: owner})
				if _, perr := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); perr != nil {
					answer("error: parking the Service %q (repointing it at the agent): %v", name, perr)
				}
				k8sPointAtSelf(ns, name, podIP, pairs)
				answer("dynamic parked")
			}
			answer("error: the Service %q exists but plug cannot read it — remove it, or grant the agent access: kubectl delete service %s", name, name)
		}
		// It's ours, but "ours" may be ANOTHER LIVE SESSION's, and with more than
		// one agent replica that session is not necessarily in this pod. The
		// annotation names the pod holding it and the port its forward answers on:
		// if that address answers, the name is taken. A dead one is a crashed
		// session's leftover, replaced.
		//
		// The port alone was the question while `app: plug` was the answer to
		// every name, and a Service an older agent created still carries nothing
		// else: fall back to dialling it here, which is exactly as far as that
		// Service's own single replica could see.
		if own := existing.Metadata.Annotations[sessionOwnerLabel]; own != "" {
			if sessionLive(own) {
				answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
			}
		} else if tp := k8sTargetPort(existing.Spec.Ports); tp != "" && agentPortLive(tp) {
			answer(nameHeldRefusal, name, heldBy(name, tp))
		}
		// Take it over IN PLACE — never delete-and-recreate. The ClusterIP is
		// handed out at creation and it is what every caller cached: patching
		// ports and endpoints (and clearing any linger stamp) keeps it, whether
		// this Service was left lingering by a clean unserve or orphaned by a
		// crash. Only if the patch itself fails do we fall back to the old
		// replace, reporting the real cause.
		patch := k8sRepointPatch(pairs, map[string]any{lingerLabel: nil, sessionOwnerLabel: owner})
		if _, perr := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); perr != nil {
			k8sDropName(ns, name)
			if _, rerr := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/services", svc, nil); rerr != nil {
				answer("error: re-provisioning the Service %q failed (a stale plug Service was removed): %v", name, rerr)
			}
		}
		k8sPointAtSelf(ns, name, podIP, pairs)
		answer("dynamic")
	default:
		answer("error: %v", err)
	}
}

// k8sRestoreParked undoes a takeover on one Service: re-patch its original
// selector+ports from the receipt annotation, and drop the annotation. Reports
// whether the Service was parked at all.
//
// The Endpoints this agent wrote are left where they are. Restoring the selector
// hands them straight back to the endpoints controller, which adopts the object
// by name and rewrites it with the workload's own pods; deleting them ourselves
// would only add a gap for the controller to fill.
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
		"metadata": map[string]any{"annotations": map[string]any{k8sParkedAnn: nil, sessionOwnerLabel: nil}},
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
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &existing); err != nil {
		// ONLY an absent Service means "nothing to drop". Any other failure —
		// 500, timeout, RBAC revoked mid-session — must not read as success: a
		// Service this session PARKED (selector repointed at the agent) would
		// stay repointed at a dead forward while the CLI reports the name
		// released, and nothing would restore it until the agent's boot gc.
		if code == 404 {
			answer("ok")
		}
		answer("error: reading the Service %q to release it: %v — anything this session parked is still parked", name, err)
	}
	if existing.Metadata.Labels[k8sManaged] == "plug" {
		// Ours: the plug-created Service LINGERS instead of dying — its
		// ClusterIP is what every caller cached (600s TTL, Docker and CoreDNS
		// alike), and a relaunch takes it over in place. See the linger block
		// by restoreServiceParked. Reaped by gc/serve once the grace passes.
		//
		// Its Endpoints go, and the owner annotation with them: a lingering name
		// belongs to nobody, and a Service with no endpoints REFUSES connections
		// where one still naming a pod that has moved on would swallow them until
		// they time out. That refusal is the behaviour the linger promises, "still
		// resolving, refusing connections like any stopped service".
		patch := map[string]any{"metadata": map[string]any{"annotations": map[string]any{
			lingerLabel:       lingerStamp(),
			sessionOwnerLabel: nil,
		}}}
		if _, err := k8sMergePatch("/api/v1/namespaces/"+ns+"/services/"+name, patch); err != nil {
			answer("error: %v", err)
		}
		_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/endpoints/"+name, nil, nil)
		answer("ok")
	}
	// A REAL Service we parked (takeover): restore it from its receipt.
	if _, err := k8sRestoreParked(ns, name, existing.Metadata.Annotations, existing.Spec.Selector); err != nil {
		answer("error: restoring the Service %q: %v", name, err)
	}
	answer("ok") // restored, or never ours to begin with
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
		gcNote("cannot list Services in %s (%v) — leftovers from crashed sessions were NOT swept", ns, err)
		return
	}
	now := time.Now()
	for _, s := range list.Items {
		if s.Metadata.Labels[k8sManaged] == "plug" {
			// Same first rule as the Swarm gc: a lingering Service within its
			// grace keeps its ClusterIP warm across even an agent restart —
			// sweeping it would kill the address the linger exists to keep.
			if stamp := s.Metadata.Annotations[lingerLabel]; stamp != "" && !lingerExpired(stamp, now) {
				continue
			}
			// The label says "a plug Service", never "MY plug Service". With one
			// agent that was the same sentence; with a replica booting beside
			// three that are serving, it is the difference between sweeping this
			// pod's leftovers and cutting a colleague's session off mid-request.
			if sessionLive(s.Metadata.Annotations[sessionOwnerLabel]) {
				continue
			}
			k8sDropName(ns, s.Metadata.Name)
			continue
		}
		// A parked REAL Service. Restoring it is no longer reserved to the agent
		// that parked it: the receipt names its owner, and an owner that no longer
		// answers is gone for good (scaled down, rescheduled, replaced), so the
		// workload it left parked is anyone's to put back. One that still answers
		// is serving right now, and used to be restored from under itself by any
		// sibling that happened to boot.
		if sessionLive(k8sReceiptOwner(s.Metadata.Annotations[k8sParkedAnn])) {
			continue
		}
		_, _ = k8sRestoreParked(ns, s.Metadata.Name, s.Metadata.Annotations, s.Spec.Selector)
	}
	k8sNoteEndpointsGrant(ns)
}
