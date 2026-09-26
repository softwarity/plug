package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// The live mount (--mount): a workload's VOLUME - a Docker volume, a bind, a
// PVC - read and written by the developer's own process, at the real path, for
// the length of a session. The one-shot projection (files-of) copies a secret's
// few files once; this is the data itself, live, both ways.
//
// How: the agent starts a HELPER beside the workload - this very image, running
// `plug-agent mount-serve` (mountserve.go) with the volume mounted at /mnt/vol -
// which serves it over SMB behind a credential minted here for this session.
// The helper is a cluster service like any other: the client reaches it
// through the tunnel it already has (a direct-tcpip channel to the address
// answered below) and mounts it with the SMB client its OS ships with. No new
// channel type, no subsystem, nothing to install on the workstation.
//
// Verbs (over SSH, one line back, like every other verb):
//
//	mount-volume <name> <volume> <agent-port> <password>
//	    start the helper for <volume> of the workload <name>. <volume> is a
//	    volume/PVC name, or the ABSOLUTE PATH it is mounted at in the workload
//	    (what the developer knows). <agent-port> is a forward this session
//	    holds, the proof it is alive - the same evidence a signpost carries.
//	    <password> is the session's SMB secret, minted by the CLIENT: after a
//	    reconnect the forward is on a new port, the helper is re-provisioned
//	    (its label is immutable, as the signpost's is), and the OS's SMB
//	    client reconnects on its own - which only works if the credential is
//	    the same one. Hex, 32 to 64 chars, inside the SSH session.
//	    Answers: mounted host=<addr> port=445 share=<s> user=<u>
//	unmount-volume <name> <volume>
//	    stop it. "ok" | "error: …"
//
// Lifecycle: the helper carries the same ownership the signpost does
// (sessionOwnerLabel), so the sweep that reaps a crashed session's signpost
// reaps its helpers too, on every backend, and the boot gc likewise. Nothing
// is parked here: mounting a volume changes nothing about the workload.
//
// On Kubernetes the helper pod is pinned to the workload pod's NODE. A
// ReadWriteOnce claim is one NODE at a time, not one pod (that is
// ReadWriteOncePod, refused below), so the helper attaches beside a running
// workload without scaling anything. The helper mounting the PVC is what the
// workload does; normal, not a defect.
const (
	mountLabel       = "plug.mount"        // this container/service/pod IS a live-mount helper
	mountOfLabel     = "plug.mount.of"     // …for this workload name
	mountVolumeLabel = "plug.mount.volume" // …serving this volume
	mountOwnerLabel  = "plug.mount.owner"  // …created by this agent (role, as signpostOwnerLabel)
	mountImageEnv    = "PLUG_MOUNT_IMAGE"  // an embedder's override, as signpostImageEnv
	mountHelperPort  = "445"
	mountShare       = "vol"
)

// mountHelperName is the helper's cluster name: one per (workload, volume),
// deterministic so a re-run finds its predecessor. The volume half is hashed:
// a volume name can be sixty characters and a bind is a path, neither of which
// fits a DNS label or a container name beside the workload's.
func mountHelperName(name, volume string) string {
	sum := sha256.Sum256([]byte(volume))
	return "plug-mnt-" + name + "-" + hex.EncodeToString(sum[:])[:8]
}

// mountImage is the image the helper runs: the agent's own, which carries
// plug-agent AND Samba, unless an embedder says otherwise (the same reasoning
// as signpostImage, and the same shape).
func mountImage(self string) string {
	if img := strings.TrimSpace(os.Getenv(mountImageEnv)); img != "" {
		return img
	}
	return self
}

// mountUser is the one SMB account the helper serves: fixed, so the verb
// validates only the password. passArgOK is that password's shape: hex, so it
// is one token and carries nothing a shell or a label would read.
const mountUser = "plug"

func passArgOK(p string) bool {
	if len(p) < 32 || len(p) > 64 {
		return false
	}
	for _, c := range p {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// mountReply is the one line the client parses. Space-separated key=value,
// like `info`: every value here is a token without spaces (an address, a
// port, a share name, hex).
func mountReply(host string) string {
	return fmt.Sprintf("mounted host=%s port=%s share=%s user=%s", host, mountHelperPort, mountShare, mountUser)
}

// volumeArgOK is what <volume> may be: a volume/PVC name (the DNS-ish shape
// both Docker and Kubernetes accept, up to 63 chars, letters/digits/-/_/.) or
// an absolute path. Anything else is refused before it reaches an API.
func volumeArgOK(v string) bool {
	if strings.HasPrefix(v, "/") {
		return len(v) > 1 && len(v) <= 255 && !strings.Contains(v, "..") && !strings.ContainsAny(v, " \t\n")
	}
	if len(v) == 0 || len(v) > 63 {
		return false
	}
	for i, c := range v {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok || i == 0 && (c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func doMountVolume(cmd []string) {
	if len(cmd) != 5 || !nameRe.MatchString(cmd[1]) || !volumeArgOK(cmd[2]) || !passArgOK(cmd[4]) {
		answer("error: usage: mount-volume <name> <volume-or-path> <agent-port> <password>")
	}
	if n, err := strconv.Atoi(cmd[3]); err != nil || n < 1 || n > 65535 {
		answer("error: %q is not a valid port", cmd[3])
	}
	mountVolume(cmd[1], cmd[2], cmd[3], cmd[4])
}

func doUnmountVolume(cmd []string) {
	if len(cmd) != 3 || !nameRe.MatchString(cmd[1]) || !volumeArgOK(cmd[2]) {
		answer("error: usage: unmount-volume <name> <volume-or-path>")
	}
	unmountVolume(cmd[1], cmd[2])
}

func mountVolume(name, volume, agentPort, pass string) {
	switch {
	case k8sAvailable():
		k8sMountVolume(k8sNamespace(), name, volume, agentPort, pass)
	case dockerAvailable():
		self, err := dockerSelf()
		if err != nil {
			answer("error: cannot identify this agent: %v", err)
		}
		if self.service != "" && swarmManager() {
			swarmMountVolume(name, volume, agentPort, pass, self)
		}
		dockerMountVolume(name, volume, agentPort, pass, self)
	}
	answer("error: this agent has no orchestrator access, so it cannot start a mount helper")
}

func unmountVolume(name, volume string) {
	switch {
	case k8sAvailable():
		if err := k8sUnmountVolume(k8sNamespace(), name, volume); err != nil {
			answer("error: %v", err)
		}
	case dockerAvailable():
		if err := dockerUnmountVolume(name, volume); err != nil {
			answer("error: %v", err)
		}
	}
	answer("ok")
}

// ── Docker (Compose, plain) ──────────────────────────────────────────────────

// dockerMount is one mount of a container as `inspect` reports it.
type dockerMount struct {
	Type        string `json:"Type"` // volume | bind | tmpfs
	Name        string `json:"Name"` // the volume's name, for Type volume
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

// pickDockerMount resolves what the developer asked for against what the
// workload actually mounts: by the path inside the container, or by the
// volume's name. A tmpfs has nothing behind it to serve. Returns the mount and
// "" on success, or nil and the list of what WAS there, for the refusal.
func pickDockerMount(want string, mounts []dockerMount) (*dockerMount, string) {
	var have []string
	for i := range mounts {
		m := &mounts[i]
		if m.Type == "tmpfs" {
			continue
		}
		label := m.Destination
		if m.Type == "volume" {
			label += " (volume " + m.Name + ")"
		} else {
			label += " (bind " + m.Source + ")"
		}
		have = append(have, label)
		if want == m.Destination || (m.Type == "volume" && want == m.Name) {
			return m, ""
		}
	}
	if len(have) == 0 {
		return nil, "it mounts nothing"
	}
	return nil, "it mounts: " + strings.Join(have, ", ")
}

// dockerWorkloadMounts finds the workload's mounts through the same candidates
// env-of uses: the parking receipt (a parked, stopped container still reports
// its mounts), a running owner, or the name as a container name.
func dockerWorkloadMounts(name string, self selfInfo) ([]dockerMount, bool) {
	for _, id := range dockerNameCandidates(name, self) {
		var insp struct {
			Mounts []dockerMount `json:"Mounts"`
		}
		if code, err := dockerAPI("GET", "/containers/"+id+"/json", nil, &insp); err == nil && code == 200 {
			return insp.Mounts, true
		}
	}
	return nil, false
}

// mountSpec is the HostConfig.Mounts entry (container) or ContainerSpec.Mounts
// entry (service) that puts the volume at /mnt/vol - the same shape for both.
func mountSpec(m *dockerMount) map[string]any {
	spec := map[string]any{"Target": mountVolumePath, "Type": m.Type}
	if m.Type == "volume" {
		spec["Source"] = m.Name
	} else {
		spec["Source"] = m.Source
	}
	return spec
}

func dockerMountVolume(name, volume, agentPort, pass string, self selfInfo) {
	nets := self.attachableNets()
	if len(nets) == 0 {
		answer("error: the agent is on no network a mount helper can join — put it on the " +
			"application network (an attachable overlay, or the Compose network your services share)")
	}
	mounts, found := dockerWorkloadMounts(name, self)
	if !found {
		answer("error: no container answers to %q here, so there is no volume to mount", name)
	}
	m, why := pickDockerMount(volume, mounts)
	if m == nil {
		answer("error: %q has no volume %q — %s", name, volume, why)
	}
	helper := mountHelperName(name, volume)
	// A helper already there is a previous session's: a live one keeps its
	// volume (one session per volume, as one session per name), a dead one is
	// swept and replaced, credential and all.
	var insp struct {
		Id     string `json:"Id"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if code, err := dockerAPI("GET", "/containers/"+helper+"/json", nil, &insp); err == nil && code == 200 {
		if sessionLive(insp.Config.Labels[sessionOwnerLabel]) {
			answer("error: %s of %q is already mounted by a live session (%s)", volume, name, insp.Config.Labels[sessionOwnerLabel])
		}
		_, _ = dockerAPI("DELETE", "/containers/"+insp.Id+"?force=1", nil, nil)
	}
	endpoints := map[string]any{}
	for _, n := range nets {
		endpoints[n] = map[string]any{"Aliases": []string{helper}}
	}
	body := map[string]any{
		"Image":      mountImage(self.image),
		"Entrypoint": []string{"/usr/local/bin/plug-agent", "mount-serve"},
		"Env":        mountEnv(pass),
		"Labels": map[string]string{
			mountLabel:        "1",
			mountOfLabel:      name,
			mountVolumeLabel:  volume,
			mountOwnerLabel:   self.owner(),
			sessionOwnerLabel: sessionOwner(self.relayTarget(), []portPair{{agent: agentPort}}),
		},
		"HostConfig": map[string]any{
			"NetworkMode":   nets[0],
			"Mounts":        []map[string]any{mountSpec(m)},
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
		},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{nets[0]: endpoints[nets[0]]}},
	}
	var created struct {
		Id string `json:"Id"`
	}
	if _, err := dockerAPI("POST", "/containers/create?name="+helper, body, &created); err != nil {
		answer("error: creating the mount helper for %s of %q: %v", volume, name, err)
	}
	for _, n := range nets[1:] {
		if _, err := dockerAPI("POST", "/networks/"+n+"/connect",
			map[string]any{"Container": created.Id, "EndpointConfig": endpoints[n]}, nil); err != nil {
			_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
			answer("error: attaching the mount helper to %s: %v", n, err)
		}
	}
	if _, err := dockerAPI("POST", "/containers/"+created.Id+"/start", nil, nil); err != nil {
		_, _ = dockerAPI("DELETE", "/containers/"+created.Id+"?force=1", nil, nil)
		answer("error: starting the mount helper: %v", err)
	}
	// Its address on the first network, rather than its name: an address needs
	// no embedded DNS to be reached, and the default bridge has none.
	var started struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	host := helper
	if code, err := dockerAPI("GET", "/containers/"+created.Id+"/json", nil, &started); err == nil && code == 200 {
		if ip := started.NetworkSettings.Networks[nets[0]].IPAddress; ip != "" {
			host = ip
		}
	}
	answer("%s", mountReply(host))
}

func mountEnv(pass string) []string {
	return []string{smbUserEnv + "=" + mountUser, smbPassEnv + "=" + pass, smbShareEnv + "=" + mountShare}
}

// dockerUnmountVolume removes the helper in whichever shape it has: a container
// (Compose, plain) or, on a manager, a service (Swarm). Absent is fine: the
// sweep may have been first, or the session never got as far as creating it.
func dockerUnmountVolume(name, volume string) error {
	helper := mountHelperName(name, volume)
	if code, err := dockerAPI("DELETE", "/containers/"+helper+"?force=1", nil, nil); err != nil && code != 404 {
		return fmt.Errorf("removing the mount helper %s: %v", helper, err)
	}
	if swarmManager() {
		if code, err := dockerAPI("DELETE", "/services/"+helper, nil, nil); err != nil && code != 404 {
			return fmt.Errorf("removing the mount helper service %s: %v", helper, err)
		}
	}
	return nil
}

// ── Swarm ────────────────────────────────────────────────────────────────────

// swarmWorkloadMounts reads the service's declared mounts, and the node its
// task last ran on - a local volume lives on ONE node, so the helper must be
// placed there to see it. The node is best-effort: a service scaled to 0 (a
// parked one) keeps its shut-down tasks listed for a while, with their NodeID.
func swarmWorkloadMounts(name string, self selfInfo) (mounts []dockerMount, node string, found bool) {
	own := swarmNameOwner(name, self)
	if own == nil {
		return nil, "", false
	}
	var svc struct {
		Spec struct {
			TaskTemplate struct {
				ContainerSpec struct {
					Mounts []struct {
						Type   string `json:"Type"`
						Source string `json:"Source"`
						Target string `json:"Target"`
					} `json:"Mounts"`
				} `json:"ContainerSpec"`
			} `json:"TaskTemplate"`
		} `json:"Spec"`
	}
	if code, err := dockerAPI("GET", "/services/"+own.id, nil, &svc); err != nil || code != 200 {
		return nil, "", false
	}
	for _, m := range svc.Spec.TaskTemplate.ContainerSpec.Mounts {
		dm := dockerMount{Type: m.Type, Source: m.Source, Destination: m.Target}
		if m.Type == "volume" {
			dm.Name = m.Source
		}
		mounts = append(mounts, dm)
	}
	var tasks []struct {
		NodeID    string `json:"NodeID"`
		CreatedAt string `json:"CreatedAt"`
	}
	f := `{"service":["` + own.id + `"]}`
	if _, err := dockerAPI("GET", "/tasks?filters="+urlEscape(f), nil, &tasks); err == nil {
		latest := ""
		for _, t := range tasks {
			if t.NodeID != "" && t.CreatedAt > latest {
				latest, node = t.CreatedAt, t.NodeID
			}
		}
	}
	return mounts, node, true
}

func swarmMountVolume(name, volume, agentPort, pass string, self selfInfo) {
	nets := self.overlayNets()
	if len(nets) == 0 {
		answer("error: the agent is on no overlay network — attach it to the overlay your services use")
	}
	mounts, node, found := swarmWorkloadMounts(name, self)
	if !found {
		answer("error: no service answers to %q here, so there is no volume to mount", name)
	}
	m, why := pickDockerMount(volume, mounts)
	if m == nil {
		answer("error: %q has no volume %q — %s", name, volume, why)
	}
	helper := mountHelperName(name, volume)
	var sp struct {
		ID   string `json:"ID"`
		Spec struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Spec"`
	}
	if code, err := dockerAPI("GET", "/services/"+helper, nil, &sp); err == nil && code == 200 {
		if sessionLive(sp.Spec.Labels[sessionOwnerLabel]) {
			answer("error: %s of %q is already mounted by a live session (%s)", volume, name, sp.Spec.Labels[sessionOwnerLabel])
		}
		_, _ = dockerAPI("DELETE", "/services/"+sp.ID, nil, nil)
	}
	var attach []map[string]any
	for _, n := range nets {
		attach = append(attach, map[string]any{"Target": n, "Aliases": []string{helper}})
	}
	task := map[string]any{
		"ContainerSpec": map[string]any{
			"Image":   pinnedImage(mountImage(self.image)),
			"Command": []string{"/usr/local/bin/plug-agent", "mount-serve"},
			"Env":     mountEnv(pass),
			"Mounts":  []map[string]any{mountSpec(m)},
		},
		"Networks":      attach,
		"RestartPolicy": map[string]any{"Condition": "any"},
	}
	if node != "" {
		task["Placement"] = map[string]any{"Constraints": []string{"node.id==" + node}}
	}
	spec := map[string]any{
		"Name": helper,
		"Labels": map[string]string{
			mountLabel:        "1",
			mountOfLabel:      name,
			mountVolumeLabel:  volume,
			mountOwnerLabel:   self.owner(),
			sessionOwnerLabel: sessionOwner(self.relayTarget(), []portPair{{agent: agentPort}}),
		},
		"TaskTemplate": task,
		"Mode":         map[string]any{"Replicated": map[string]any{"Replicas": 1}},
	}
	if _, err := dockerAPI("POST", "/services/create", spec, nil); err != nil {
		answer("error: creating the mount helper service for %s of %q: %v", volume, name, err)
	}
	// The service name: the overlay's embedded DNS answers it with the VIP, and
	// the client's dial goes through this agent's resolver on that overlay.
	answer("%s", mountReply(helper))
}

// ── Kubernetes ───────────────────────────────────────────────────────────────

// k8sClaimVolume is the sliver of a pod volume this reads: a PVC's claim.
type k8sClaimVolume struct {
	Name string `json:"name"`
	PVC  *struct {
		ClaimName string `json:"claimName"`
	} `json:"persistentVolumeClaim"`
}

// pickClaim resolves <volume> against a pod's PVC-backed mounts: by mount
// path, or by claim name. Returns the claim name and "", or "" and what was
// there, for the refusal.
func pickClaim(want string, vols []k8sClaimVolume, mounts []k8sMount) (string, string) {
	claimOf := map[string]string{}
	for _, v := range vols {
		if v.PVC != nil && v.PVC.ClaimName != "" {
			claimOf[v.Name] = v.PVC.ClaimName
		}
	}
	var have []string
	for _, m := range mounts {
		c, ok := claimOf[m.Name]
		if !ok {
			continue
		}
		have = append(have, m.MountPath+" (pvc "+c+")")
		if want == m.MountPath || want == c {
			return c, ""
		}
	}
	if len(have) == 0 {
		return "", "it mounts no PersistentVolumeClaim"
	}
	return "", "it mounts: " + strings.Join(have, ", ")
}

// k8sWorkloadClaim finds the workload pod behind <name> (the same route
// files-of takes: the Service's selector, or the receipt's when parked) and
// resolves the claim, along with the node the pod runs on.
func k8sWorkloadClaim(ns, name, volume string) (claim, node, why string) {
	var svc struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/services/"+name, nil, &svc); err != nil || code != 200 {
		return "", "", fmt.Sprintf("no Service %q in %s", name, ns)
	}
	sel := svc.Spec.Selector
	if raw := svc.Metadata.Annotations[k8sParkedAnn]; raw != "" {
		var r k8sReceipt
		if json.Unmarshal([]byte(raw), &r) == nil && len(r.Selector) > 0 {
			sel = r.Selector
		}
	}
	if len(sel) == 0 {
		return "", "", fmt.Sprintf("Service %q selects no pod", name)
	}
	var pods struct {
		Items []struct {
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
			Spec struct {
				NodeName   string           `json:"nodeName"`
				Volumes    []k8sClaimVolume `json:"volumes"`
				Containers []struct {
					VolumeMounts []k8sMount `json:"volumeMounts"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"items"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/pods?labelSelector="+url.QueryEscape(labelSelector(sel)), nil, &pods); err != nil || code != 200 {
		return "", "", fmt.Sprintf("cannot list the pods behind %q", name)
	}
	for _, p := range pods.Items {
		if len(p.Spec.Containers) == 0 {
			continue
		}
		var mounts []k8sMount
		for _, c := range p.Spec.Containers {
			mounts = append(mounts, c.VolumeMounts...)
		}
		c, w := pickClaim(volume, p.Spec.Volumes, mounts)
		if c != "" {
			return c, p.Spec.NodeName, ""
		}
		why = fmt.Sprintf("%q has no volume %q — %s", name, volume, w)
	}
	if why == "" {
		why = fmt.Sprintf("no pod behind %q", name)
	}
	return "", "", why
}

// k8sClaimModes reads a claim's access modes: ReadWriteOncePod is the one that
// cannot be shared with the workload at all, and the one to say so about.
func k8sClaimModes(ns, claim string) []string {
	var pvc struct {
		Spec struct {
			AccessModes []string `json:"accessModes"`
		} `json:"spec"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/persistentvolumeclaims/"+claim, nil, &pvc); err != nil || code != 200 {
		return nil
	}
	return pvc.Spec.AccessModes
}

// k8sMountPod is the helper pod. Labels carry what a label value may (the
// flag, the workload's name, a folded volume); the session owner is host:port,
// which a label value cannot hold, so it rides an annotation - as the parking
// receipt's owner does.
func k8sMountPod(ns, helper, name, volume, claim, node, image, owner, pass string) map[string]any {
	env := []map[string]string{}
	for _, kv := range mountEnv(pass) {
		k, v, _ := strings.Cut(kv, "=")
		env = append(env, map[string]string{"name": k, "value": v})
	}
	spec := map[string]any{
		"restartPolicy": "Always",
		"volumes": []map[string]any{{
			"name":                  "vol",
			"persistentVolumeClaim": map[string]any{"claimName": claim},
		}},
		"containers": []map[string]any{{
			"name":         "mount",
			"image":        image,
			"command":      []string{"/usr/local/bin/plug-agent", "mount-serve"},
			"env":          env,
			"ports":        []map[string]any{{"containerPort": 445}},
			"volumeMounts": []map[string]any{{"name": "vol", "mountPath": mountVolumePath}},
		}},
	}
	if node != "" {
		spec["nodeName"] = node
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      helper,
			"namespace": ns,
			"labels": map[string]string{
				"app":            "plug-mount",
				mountLabel:       "1",
				mountOfLabel:     name,
				mountVolumeLabel: labelSafe(volume),
			},
			"annotations": map[string]string{
				sessionOwnerLabel: owner,
				mountVolumeLabel:  volume,
			},
		},
		"spec": spec,
	}
}

// labelSafe folds a value into what a label value accepts: a path is not one.
func labelSafe(v string) string {
	if strings.HasPrefix(v, "/") {
		sum := sha256.Sum256([]byte(v))
		return "path-" + hex.EncodeToString(sum[:])[:12]
	}
	return v
}

func k8sMountVolume(ns, name, volume, agentPort, pass string) {
	claim, node, why := k8sWorkloadClaim(ns, name, volume)
	if claim == "" {
		answer("error: %s", why)
	}
	for _, m := range k8sClaimModes(ns, claim) {
		if m == "ReadWriteOncePod" {
			answer("error: claim %q is ReadWriteOncePod — one pod at a time, so no helper can mount it beside %q", claim, name)
		}
	}
	_, _, image, _, err := k8sAgentDeployment()
	if err != nil || image == "" {
		answer("error: cannot tell this agent's image, which the mount helper runs: %v", err)
	}
	helper := mountHelperName(name, volume)
	owner := sessionOwner(k8sSelfIP(), []portPair{{agent: agentPort}})
	var existing struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if code, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/pods/"+helper, nil, &existing); err == nil && code == 200 {
		if sessionLive(existing.Metadata.Annotations[sessionOwnerLabel]) {
			answer("error: %s of %q is already mounted by a live session (%s)", volume, name, existing.Metadata.Annotations[sessionOwnerLabel])
		}
		if err := k8sDeletePodAndWait(ns, helper, 30*time.Second); err != nil {
			answer("error: replacing the previous mount helper: %v", err)
		}
	}
	body := k8sMountPod(ns, helper, name, volume, claim, node, mountImage(image), owner, pass)
	code, err := k8sAPI("POST", "/api/v1/namespaces/"+ns+"/pods", body, nil)
	if code == 403 {
		answer("error: this agent's RBAC cannot create pods, which a mount helper is — re-apply deploy/plug-k8s.yaml (pods: create, delete)")
	}
	if err != nil {
		answer("error: creating the mount helper pod: %v", err)
	}
	// Its address, once it has one. Scheduling is seconds; the client keeps
	// probing the port after this, so only the address is waited for here.
	ip := ""
	for i := 0; i < 60 && ip == ""; i++ {
		var pod struct {
			Status struct {
				PodIP string `json:"podIP"`
				Phase string `json:"phase"`
			} `json:"status"`
		}
		if c, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/pods/"+helper, nil, &pod); err == nil && c == 200 {
			ip = pod.Status.PodIP
			if pod.Status.Phase == "Failed" {
				break
			}
		}
		if ip == "" {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if ip == "" {
		_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/pods/"+helper, nil, nil)
		answer("error: the mount helper pod got no address in 30s — is the claim %q bindable on node %q?", claim, node)
	}
	answer("%s", mountReply(ip))
}

func k8sUnmountVolume(ns, name, volume string) error {
	helper := mountHelperName(name, volume)
	code, err := k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/pods/"+helper, nil, nil)
	if err != nil && code != 404 {
		return fmt.Errorf("removing the mount helper pod %s: %v", helper, err)
	}
	return nil
}

// k8sDeletePodAndWait deletes and waits for the name to be free, which a
// re-create needs: a pod being terminated still holds its name.
func k8sDeletePodAndWait(ns, pod string, d time.Duration) error {
	if code, err := k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/pods/"+pod, nil, nil); err != nil && code != 404 {
		return err
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if code, _ := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/pods/"+pod, nil, nil); code == 404 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("pod %s is still terminating after %s", pod, d)
}

// ── Reaping ──────────────────────────────────────────────────────────────────

// sweepMountHelpers is the live-mount half of every sweep (boot and periodic):
// a helper whose session no longer answers is a crashed or killed session's
// leftover, and it goes. Nothing to restore - a mount parks nothing - so this
// is simpler than the signpost's sweep; the ownership rule is the same.
func sweepMountHelpers() {
	switch {
	case k8sAvailable():
		k8sSweepMountHelpers(k8sNamespace())
	case dockerAvailable():
		dockerSweepMountHelpers()
	}
}

func dockerSweepMountHelpers() {
	f := `{"label":["` + mountLabel + `=1"]}`
	var clist []struct {
		Id     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
	}
	if _, err := dockerAPI("GET", "/containers/json?all=1&filters="+urlEscape(f), nil, &clist); err == nil {
		for _, c := range clist {
			if !sessionLive(c.Labels[sessionOwnerLabel]) {
				_, _ = dockerAPI("DELETE", "/containers/"+c.Id+"?force=1", nil, nil)
			}
		}
	}
	if !swarmManager() {
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
			if !sessionLive(s.Spec.Labels[sessionOwnerLabel]) {
				_, _ = dockerAPI("DELETE", "/services/"+s.ID, nil, nil)
			}
		}
	}
}

func k8sSweepMountHelpers(ns string) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if _, err := k8sAPI("GET", "/api/v1/namespaces/"+ns+"/pods?labelSelector="+url.QueryEscape(mountLabel+"=1"), nil, &list); err != nil {
		return
	}
	for _, p := range list.Items {
		if !sessionLive(p.Metadata.Annotations[sessionOwnerLabel]) {
			_, _ = k8sAPI("DELETE", "/api/v1/namespaces/"+ns+"/pods/"+p.Metadata.Name, nil, nil)
		}
	}
}
