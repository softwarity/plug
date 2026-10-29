package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roleRules reads the Role of deploy/plug-k8s.yaml as "resource:verb", the
// resource group-qualified outside the core group, as k8sGrantProbe writes
// them. A reader of the manifest's own flow-list shape, not of YAML: the test
// is about which rules are there, and that shape is what the file uses.
func roleRules(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "deploy", "plug-k8s.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	list := func(line, key string) []string {
		v, ok := strings.CutPrefix(strings.TrimPrefix(strings.TrimSpace(line), "- "), key+":")
		if !ok {
			return nil
		}
		v = strings.Trim(strings.TrimSpace(v), "[]")
		var out []string
		for _, f := range strings.Split(v, ",") {
			out = append(out, strings.Trim(strings.TrimSpace(f), `"`))
		}
		return out
	}
	var rules []string
	inRole := false
	var groups, resources []string
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "kind: "):
			inRole = line == "kind: Role"
			continue
		case !inRole || strings.HasPrefix(strings.TrimSpace(line), "#"):
			continue
		}
		if g := list(line, "apiGroups"); g != nil {
			groups, resources = g, nil
		}
		if r := list(line, "resources"); r != nil {
			resources = r
		}
		if verbs := list(line, "verbs"); verbs != nil {
			for _, g := range groups {
				for _, r := range resources {
					if g != "" {
						r = g + "/" + r
					}
					for _, v := range verbs {
						rules = append(rules, r+":"+v)
					}
				}
			}
		}
	}
	if len(rules) == 0 {
		t.Fatal("no rule read from the Role of deploy/plug-k8s.yaml: has its shape changed?")
	}
	return rules
}

// Every rule the shipped Role grants is vouched for by a probe `plug doctor`
// reads, or named as one no probe needs. A rule added with a feature and no
// probe is how the live mount's went unchecked: the doctor said "every rule
// this agent's version needs is granted" to a Role that could not create its
// helper pod. Both ways round, so a probe cannot go on vouching for a rule
// the Role has dropped.
func TestEveryRuleOfTheShippedRoleIsProbed(t *testing.T) {
	known := map[string]bool{}
	for _, r := range k8sUnprobed {
		known[r] = true
	}
	for _, p := range k8sGrantProbes {
		for _, r := range p.vouches {
			known[r] = true
		}
	}
	inRole := map[string]bool{}
	for _, r := range roleRules(t) {
		inRole[r] = true
		if !known[r] {
			t.Errorf("the Role grants %s and no probe vouches for it: add it to a k8sGrantProbe (or to k8sUnprobed, saying why)", r)
		}
	}
	for r := range known {
		if !inRole[r] {
			t.Errorf("%s is vouched for, but the Role no longer grants it", r)
		}
	}
}

// The live mount's rules, missing one at a time: each is a missing grant, and
// the probe leaves nothing behind when they are all there.
func TestMountGrantProbeNamesEachMissingRule(t *testing.T) {
	cases := []struct{ name, method, path string }{
		{"no pods create", "POST", "/api/v1/namespaces/ns/pods"},
		{"no pods delete", "DELETE", "/api/v1/namespaces/ns/pods/plug-mount-grant-probe"},
		{"no claims get", "GET", "/api/v1/namespaces/ns/persistentvolumeclaims/plug-mount-grant-probe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fa := newFakeAPIServer(t, "ns", "10.0.0.5")
			fa.refuseWith(func(method, path string) (int, string) {
				if method == c.method && path == c.path {
					return 403, `pods is forbidden: User "system:serviceaccount:ns:plug" cannot ` + strings.ToLower(c.method) + ` resource "pods"`
				}
				return 0, ""
			})
			if k8sMountGranted("ns") {
				t.Errorf("granted with %s refused", c.method+" "+c.path)
			}
		})
	}
	// OpenShift's SCC refusing the probe's pod is no word on the Role.
	fa := newFakeAPIServer(t, "ns", "10.0.0.5")
	fa.refuseWith(func(method, path string) (int, string) {
		if method == "POST" {
			return 403, `pods "plug-mount-grant-probe" is forbidden: unable to validate against any security context constraint`
		}
		return 0, ""
	})
	if !k8sMountGranted("ns") {
		t.Error("an admission plugin's 403 was taken for a missing rule")
	}
	fa = newFakeAPIServer(t, "ns", "10.0.0.5")
	fa.add("pods", map[string]any{"metadata": map[string]any{"name": "plug-mount-grant-probe"}})
	if !k8sMountGranted("ns") {
		t.Fatal("not granted with every rule in place")
	}
	if fa.get("pods", "plug-mount-grant-probe") == nil {
		t.Error("the probe deleted a pod that carried its name: its delete must run dry")
	}
	if len(fa.collection("pods")) != 1 {
		t.Errorf("the probe left pods behind: %v", fa.collection("pods"))
	}
}
