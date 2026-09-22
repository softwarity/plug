package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// plug mcp: the facts plug knows, served to an AI coding agent as tools with
// structured answers. What an agent lacks is not the ability to run
// `plug -s …`, it is knowing what to run and reading what came of it: which
// names the cluster has, who holds one, what doctor says, which variables a
// workload runs with. plug knows all of that and until now answered in prose
// for a person. Nothing here is new on the agent side: every tool is a verb the
// CLI already speaks, or the doctor it already runs.
//
// stdio, the way IDEs launch an MCP server: one process per editor session,
// spoken to over its own stdin/stdout. Nothing listens on a port.

func cmdMCP(args []string) {
	if len(args) > 0 {
		fatal("usage: plug mcp   (an MCP server over stdio; add it to your editor's MCP config)")
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "plug", Version: version}, nil)

	mcp.AddTool(s, &mcp.Tool{Name: "list_profiles",
		Description: "The clusters this machine knows: each profile's name, agent address and update policy. A profile is what -p names."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mcpProfiles, error) {
			out := mcpProfiles{}
			for _, p := range listProfiles() {
				cfg, err := readProfileSoft(p)
				e := mcpProfile{Name: p}
				if err != nil {
					e.Error = err.Error()
				} else {
					e.Agent = cfg.host + ":" + cfg.port
					e.UpdateMode = cfg.updateMode
					e.HasKey = cfg.key != ""
				}
				out.Profiles = append(out.Profiles, e)
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "doctor",
		Description: "plug's own diagnosis of this machine and, per profile, of the cluster: privilege, resolver, agent reachability and version, RBAC grants (endpoints, exec), sessions. Each check carries a status (ok, warn, fail), a detail and, when something is wrong, the exact remedy."},
		func(_ context.Context, _ *mcp.CallToolRequest, in mcpDoctorIn) (*mcp.CallToolResult, mcpDoctorOut, error) {
			var checks []check
			add := func(c check) { checks = append(checks, c) }
			doctorLocal(add)
			profiles := listProfiles()
			if in.Profile != "" {
				profiles = []string{in.Profile}
			}
			for _, p := range profiles {
				doctorProfile(p, add)
			}
			out := mcpDoctorOut{}
			for _, c := range checks {
				out.Checks = append(out.Checks, mcpCheck{Area: c.area, Name: c.name, Status: statusWord(c.status), Detail: c.detail, Remedy: c.remedy})
				switch c.status {
				case stFail:
					out.Fails++
				case stWarn:
					out.Warns++
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "env_of",
		Description: "The environment a deployed workload runs with in the cluster, as plug hands it to a process that takes the workload's place with -s: KEY=VALUE pairs, the container's own plumbing (PATH, KUBERNETES_*) already left out. Values that look like secrets are masked unless reveal is true."},
		func(_ context.Context, _ *mcp.CallToolRequest, in mcpEnvIn) (*mcp.CallToolResult, mcpEnvOut, error) {
			cfg, err := readProfileSoft(in.Profile)
			if err != nil {
				return nil, mcpEnvOut{}, err
			}
			if err := checkExposeName(in.Name); err != nil {
				return nil, mcpEnvOut{}, err
			}
			tr, err := dialTunnel(cfg)
			if err != nil {
				return nil, mcpEnvOut{}, fmt.Errorf("reach the agent for profile %q: %w", in.Profile, err)
			}
			defer tr.Close()
			raw, err := tr.ExecAll("env-of " + in.Name)
			if err != nil {
				return nil, mcpEnvOut{}, err
			}
			if strings.HasPrefix(raw, "error:") {
				return nil, mcpEnvOut{}, fmt.Errorf("agent: %s", strings.TrimSpace(strings.TrimPrefix(raw, "error:")))
			}
			out := mcpEnvOut{Name: in.Name, Vars: map[string]string{}}
			for _, l := range strings.Split(raw, "\n") {
				l = strings.TrimRight(l, "\r")
				switch {
				case l == "":
				case strings.HasPrefix(l, "# "):
					out.Notes = append(out.Notes, strings.TrimPrefix(l, "# "))
				default:
					if k, v, ok := strings.Cut(l, "="); ok {
						if !in.Reveal && looksSecret(k) {
							v = "***"
							out.Masked = append(out.Masked, k)
						}
						out.Vars[k] = v
					}
				}
			}
			return nil, out, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "resolve_name",
		Description: "Whether a name exists in the cluster, resolved through the cluster's own DNS by the agent: found or nxdomain. What -s would take over, or what a session would reach."},
		func(_ context.Context, _ *mcp.CallToolRequest, in mcpNameIn) (*mcp.CallToolResult, mcpResolveOut, error) {
			cfg, err := readProfileSoft(in.Profile)
			if err != nil {
				return nil, mcpResolveOut{}, err
			}
			if err := checkExposeName(in.Name); err != nil {
				return nil, mcpResolveOut{}, err
			}
			tr, err := dialTunnel(cfg)
			if err != nil {
				return nil, mcpResolveOut{}, fmt.Errorf("reach the agent for profile %q: %w", in.Profile, err)
			}
			defer tr.Close()
			ans, err := tr.Exec("resolve " + in.Name)
			if err != nil {
				return nil, mcpResolveOut{}, err
			}
			return nil, mcpResolveOut{Name: in.Name, Answer: strings.TrimSpace(ans), Exists: strings.HasPrefix(ans, "found")}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "agent_info",
		Description: "The agent's own report for a profile: version, backend (docker, docker-swarm, kubernetes), image, and the RBAC grants it probed (endpoints, exec) on Kubernetes."},
		func(_ context.Context, _ *mcp.CallToolRequest, in mcpProfileIn) (*mcp.CallToolResult, mcpInfoOut, error) {
			cfg, err := readProfileSoft(in.Profile)
			if err != nil {
				return nil, mcpInfoOut{}, err
			}
			tr, err := dialTunnel(cfg)
			if err != nil {
				return nil, mcpInfoOut{}, fmt.Errorf("reach the agent for profile %q: %w", in.Profile, err)
			}
			defer tr.Close()
			ans, err := tr.Exec("info")
			if err != nil {
				return nil, mcpInfoOut{}, err
			}
			out := mcpInfoOut{Fields: map[string]string{}}
			for _, f := range strings.Fields(ans) {
				if k, v, ok := strings.Cut(f, "="); ok {
					out.Fields[k] = v
				}
			}
			return nil, out, nil
		})

	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "plug mcp:", err)
		os.Exit(1)
	}
}

type mcpProfile struct {
	Name       string `json:"name"`
	Agent      string `json:"agent,omitempty"`
	UpdateMode string `json:"update_mode,omitempty"`
	HasKey     bool   `json:"has_personal_key"`
	Error      string `json:"error,omitempty"`
}
type mcpProfiles struct {
	Profiles []mcpProfile `json:"profiles"`
}
type mcpDoctorIn struct {
	Profile string `json:"profile,omitempty" jsonschema:"only this profile; every profile when empty"`
}
type mcpCheck struct {
	Area   string `json:"area"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Remedy string `json:"remedy,omitempty"`
}
type mcpDoctorOut struct {
	Checks []mcpCheck `json:"checks"`
	Fails  int        `json:"fails"`
	Warns  int        `json:"warns"`
}
type mcpProfileIn struct {
	Profile string `json:"profile" jsonschema:"the profile name, as -p takes it"`
}
type mcpNameIn struct {
	Profile string `json:"profile" jsonschema:"the profile name, as -p takes it"`
	Name    string `json:"name" jsonschema:"a cluster name, single label"`
}
type mcpEnvIn struct {
	Profile string `json:"profile" jsonschema:"the profile name, as -p takes it"`
	Name    string `json:"name" jsonschema:"the deployed workload's name in the cluster"`
	Reveal  bool   `json:"reveal,omitempty" jsonschema:"return secret-looking values in the clear; masked by default"`
}
type mcpEnvOut struct {
	Name   string            `json:"name"`
	Vars   map[string]string `json:"vars"`
	Masked []string          `json:"masked,omitempty"`
	Notes  []string          `json:"notes,omitempty"`
}
type mcpResolveOut struct {
	Name   string `json:"name"`
	Exists bool   `json:"exists"`
	Answer string `json:"answer"`
}
type mcpInfoOut struct {
	Fields map[string]string `json:"fields"`
}

// looksSecret is the mask rule: a key that names a password, a token, a key or
// a secret is not shown to an agent by default. It errs on the side of masking;
// reveal=true is one word away for the person who decides.
func looksSecret(key string) bool {
	k := strings.ToUpper(key)
	for _, m := range []string{"PASSWORD", "PASSWD", "PWD", "SECRET", "TOKEN", "API_KEY", "APIKEY", "PRIVATE_KEY", "CREDENTIAL", "AUTH"} {
		if strings.Contains(k, m) {
			return true
		}
	}
	return false
}

// statusWord is the check status as a word an agent can branch on.
func statusWord(s checkStatus) string {
	switch s {
	case stFail:
		return "fail"
	case stWarn:
		return "warn"
	}
	return "ok"
}

// checkExposeName is the -s name rule, applied to what an agent asks about: a
// name that -s would refuse is refused here too, before it reaches the agent.
func checkExposeName(name string) error {
	if !exposeName.MatchString(name) {
		return fmt.Errorf("%q is not a cluster name (a single DNS label: letters, digits, dashes)", name)
	}
	return nil
}
