package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// Everything specific to Docker Swarm: the manager check, the service lookups,
// the self-update that goes through a service spec rather than a container.
//
// The API calls themselves live in docker.go, because Swarm speaks the Docker
// Engine API; what is here is the part that only makes sense with a manager and
// a service on the other end.

// swarmSelfUpdate rolls the agent's own service. A pinned RELEASE tag is moved
// to the newest release (that is the whole point — re-resolving a pin can only
// ever return the same image); a moving tag is left as it is and merely
// re-resolved. Either way the pinned digest is dropped from the image (stack
// deploy pins one — with it, no update ever changes anything) and ForceUpdate
// rolls the task even when the digest comes back unchanged.
func swarmSelfUpdate(self selfInfo, decide func(string) (string, string, string)) {
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
		// The digest is NOT stripped here: a digest-only pin must reach the
		// decision, where it means "release pin", not the `latest` a naively
		// stripped repo reads as.
	}
	target, plan, note := decide(img)
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
		if s.Spec.Labels[signpostLabel] == "1" {
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

func swarmServe(name string, pairs []portPair, self selfInfo) {
	// Two refusals used to stand here: more than one replica, and global mode.
	// Both existed because the signpost relayed to the service VIP, which load
	// balances across every task while a session's forward lives on exactly one -
	// so -s was a lottery past a single task, and global mode hid it (one task per
	// node, and no replica count that would have shown it). relayTarget names the
	// TASK now, so neither refusal has anything left to protect, and the counting
	// they needed went with them. What survives of the question is per-NAME rather
	// than per-agent, and is answered below where it belongs: a name a live
	// session already holds is refused, whichever task that session landed on.
	nets := self.overlayNets()
	if len(nets) == 0 {
		// Only ingress/bridge — nothing to publish an alias on.
		answer("error: the agent is on no overlay network — attach it to the overlay your " +
			"services use, otherwise the name cannot resolve for them")
	}
	// Serving is the moment the agent runs code anyway: reap the lingers whose
	// grace has passed, THIS name's included — if ours expired, the GET below
	// sees nothing and a fresh create (fresh VIP) is the honest outcome.
	sweepExpiredServiceLingers()
	// A signpost service already carrying this name may belong to a LIVE
	// session — its relay port still answers on this agent — and then the name
	// is taken; a dead port is a crashed session's leftover, swept below.
	var sp struct {
		ID      string `json:"ID"`
		Version struct {
			Index uint64 `json:"Index"`
		} `json:"Version"`
		Spec struct {
			Labels       map[string]string `json:"Labels"`
			TaskTemplate struct {
				ContainerSpec struct {
					Command []string `json:"Command"`
				} `json:"ContainerSpec"`
			} `json:"TaskTemplate"`
		} `json:"Spec"`
	}
	// Whether we can UPDATE the signpost that is already there instead of
	// replacing it. Swarm gives a service its VIP once, at creation, and that VIP
	// is what every caller resolved and cached — recreating hands out a new one
	// and every cached answer points at an address that no longer exists. The
	// callers recover when their cache expires, which is why this looks like a
	// name that "comes back on its own".
	//
	// It matters far more than one session: re-provisioning after a reconnect
	// goes through here too, so today a laptop waking up is enough to move the
	// VIP. Kubernetes already keeps its ClusterIP across park and restore; this
	// brings Swarm in line.
	reuse := false
	if code, err := dockerAPI("GET", "/services/"+signpostName(name), nil, &sp); err == nil && code == 200 {
		// Same rule as the container shape: a service name is cluster-wide, so
		// another agent's LIVE signpost must not read as our leftover just
		// because its port does not answer in our netns.
		if o := sp.Spec.Labels[signpostOwnerLabel]; o != "" && o != self.owner() && ownerAlive(o, true) {
			answer("error: %q is served here by another plug agent (%s), which is still running — "+
				"two agents on one cluster cannot both own a name. Use a different name, or stop that agent.", name, o)
		}
		// Dial the session's own address: a sibling TASK of this same service
		// shares our owner label, so the check above waves it through, and its
		// forward answers on the overlay and never in this task's loopback.
		if own := signpostOwner(sp.Spec.Labels, sp.Spec.TaskTemplate.ContainerSpec.Command); sessionLive(own) {
			answer(nameHeldRefusal, name, heldBy(name, ownerPort(own)))
		}
		// Past those two checks the signpost is ours to take: nobody live is
		// behind it. Reuse it UNLESS it carries a parking receipt — that receipt
		// is what scales a real workload back up, and deleting the signpost is
		// how the restore is driven (see restoreServiceParked). Keeping the VIP
		// is not worth risking a deployed service left at zero replicas.
		reuse = sp.ID != "" && sp.Spec.Labels[parkedServiceLabel] == ""
	}
	// A leftover signpost service (a crashed session's, or a re-run) may carry a
	// parking receipt: restore it FIRST, then re-detect — one restore path, and
	// the takeover below re-parks with a fresh receipt. Skipped when we are
	// reusing: there is no receipt to act on (that is what made it reusable),
	// and this is the call that would delete the service and take the VIP with it.
	if !reuse {
		if err := restoreServiceParked(name); err != nil {
			answer("error: restoring what the previous %s session parked: %v", name, err)
		}
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
		signpostLabel:      "1",
		signpostOwnerLabel: self.owner(),
		sessionOwnerLabel:  sessionOwner(self.relayTarget(), pairs),
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
				"Image":   pinnedImage(self.image),
				"Command": signpostArgs(pairs, self.relayTarget()),
			},
			"Networks":      attach,
			"RestartPolicy": map[string]any{"Condition": "any"},
		},
		"Mode": map[string]any{"Replicated": map[string]any{"Replicas": 1}},
		// stop-first, explicitly. A signpost relays to ONE agent port, so two
		// tasks behind the VIP is not a smoother rollout — it is half the
		// connections landing on the previous task, which relays to a port that
		// no longer answers. A brief gap is the honest shape here.
		"UpdateConfig": map[string]any{"Order": "stop-first"},
	}
	if reuse {
		// The VIP is kept by updating in place. Swarm requires the version we
		// read the service at, which is also the concurrency guard: if anything
		// touched it since, this fails rather than clobbering.
		path := fmt.Sprintf("/services/%s/update?version=%d", sp.ID, sp.Version.Index)
		if _, err := dockerAPI("POST", path, spec, nil); err != nil {
			answer("error: updating the %s signpost service: %v", name, err)
		}
	} else if _, err := dockerAPI("POST", "/services/create", spec, nil); err != nil {
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

// serviceExists reports whether a Swarm service named `name` is present.
func serviceExists(name string) bool {
	code, err := dockerAPI("GET", "/services/"+name, nil, nil)
	return err == nil || code != 404
}
