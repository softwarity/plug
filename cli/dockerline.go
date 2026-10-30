package main

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The user's `docker run` line, read as docker reads it.
//
// The container joins the sidecar's network (--network container:), and docker
// refuses, or worse ignores, every option that would give it a network of its
// own: -p and -h are refused, a second --network silently wins over ours and
// takes the container out of the cluster. Those options mean something all
// the same, and it is the sidecar's: it owns the network the container lives
// in. So they are moved there, and `-p 3000:3000` publishes the port as it
// would on any container. The other options the line carries stay where they
// were written, untouched; the only one read is a mount, whose path the
// container then has from the user, not from the workload.
//
// Reading the line means knowing which options take a value, and a table of
// them would rot: docker adds options every release (--use-api-socket is
// recent), and one boolean missing from the table reads the image as its value
// and the command as the image. So the table is asked of the docker CLI that
// will run the line, from `docker run --help`, where an option that takes a
// value is followed by its type and a boolean is not.

// dockerRunOptions is what `docker run --help` says of each option: whether it
// takes a value, by long name, and the long name of each shorthand.
type dockerRunOptions struct {
	valued map[string]bool
	short  map[byte]string
}

// dockerHelpOption matches one option line of `docker run --help`:
// "  -p, --publish list    Publish…" or "      --rm    Automatically…". The
// type, when there is one, is a single word after one space, and the
// description is two spaces or more away.
var dockerHelpOption = regexp.MustCompile(`^\s+(?:-([A-Za-z0-9]), )?--([a-z0-9][a-z0-9-]*)(?: ([a-z0-9][a-z0-9-]*(?:\[[^\]]*\])?))?(?:\s{2,}|$)`)

// parseDockerRunHelp reads the options out of `docker run --help`. Nothing read
// is nil: a help this cannot read is no table to read a line with.
func parseDockerRunHelp(help string) *dockerRunOptions {
	o := &dockerRunOptions{valued: map[string]bool{}, short: map[byte]string{}}
	for _, line := range strings.Split(help, "\n") {
		m := dockerHelpOption.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		o.valued[m[2]] = m[3] != ""
		if m[1] != "" {
			o.short[m[1][0]] = m[2]
		}
	}
	if len(o.valued) == 0 {
		return nil
	}
	// The two spellings docker still accepts and no longer lists. Frozen: a
	// deprecated alias does not change.
	for alias := range dockerOptionAliases {
		o.valued[alias] = true
	}
	return o
}

// dockerOptionAliases are the hidden spellings, by the option they mean.
var dockerOptionAliases = map[string]string{"net": "network", "net-alias": "network-alias"}

// dockerSidecarOptions are the options of the network the container shares,
// which the sidecar is given in its place.
var dockerSidecarOptions = map[string]bool{
	"publish": true, "expose": true,
	"network": true, "network-alias": true, "ip": true, "ip6": true, "link-local-ip": true, "mac-address": true, "link": true,
	"hostname": true, "domainname": true, "add-host": true,
}

// dockerRunLine is the line split between the two containers.
type dockerRunLine struct {
	sidecar []string // the network's options, for the sidecar
	user    []string // the rest, options, image and command, as the container gets them
	targets []string // the container paths the user's own mounts take
	detach  bool     // the line asked for -d, which is left out
}

// dockerRunOption is one option of the line: its long name, its value if it
// has one, and the token as written when it is not one docker listed.
type dockerRunOption struct {
	name, value string
	hasValue    bool
	raw         string
}

func (o dockerRunOption) tokens() []string {
	switch {
	case o.raw != "":
		return []string{o.raw}
	case o.hasValue:
		return []string{"--" + o.name + "=" + o.value}
	}
	return []string{"--" + o.name}
}

// readDockerRunLine splits the line after `docker run`. The options end at
// the image, the first word that is not one, or at "--"; from there on
// everything is the container's and goes through as written. So does every
// option that stays: only what moves to the sidecar is written anew, and a
// cluster of shorthands that holds one (-itp 8080:8080) is spelled out long.
//
// -d is left out. The container runs in the foreground because plug does: the
// session lasts as long as plug, and plug as long as the container, which a
// detached one would outlive on a network that is gone. Asking for it is only
// asking for the terminal back, which costs nothing to drop.
func readDockerRunLine(args []string, opts *dockerRunOptions) (dockerRunLine, error) {
	// One group per word of the line (two for an option and its value): the
	// options it holds, and the words as written.
	type group struct {
		options []dockerRunOption
		raw     []string
	}
	var groups []group
	rest := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			rest = args[i:]
			break
		}
		g := group{raw: []string{a}}
		next := func(name string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("--dockerrun: --%s on your `docker run` line has no value", name)
			}
			i++
			g.raw = append(g.raw, args[i])
			return args[i], nil
		}
		if long, ok := strings.CutPrefix(a, "--"); ok {
			name, value, has := strings.Cut(long, "=")
			valued, known := opts.valued[name]
			if canon, ok := dockerOptionAliases[name]; ok {
				name = canon
			}
			if known {
				if valued && !has {
					v, err := next(name)
					if err != nil {
						return dockerRunLine{}, err
					}
					value, has = v, true
				}
				// Not an option this docker has is left to docker, which
				// says so in its words.
				g.options = append(g.options, dockerRunOption{name: name, value: value, hasValue: has})
			}
			groups = append(groups, g)
			continue
		}
		// A cluster of shorthands, -it or -itp 80:80: booleans until one that
		// takes a value, which is the rest of the cluster or the next word.
		for j := 1; j < len(a); j++ {
			name, ok := opts.short[a[j]]
			if !ok {
				g.options = append(g.options, dockerRunOption{raw: "-" + a[j:]})
				break
			}
			if opts.valued[name] {
				value := strings.TrimPrefix(a[j+1:], "=")
				if value == "" {
					v, err := next(name)
					if err != nil {
						return dockerRunLine{}, err
					}
					value = v
				}
				g.options = append(g.options, dockerRunOption{name: name, value: value, hasValue: true})
				break
			}
			if j+1 < len(a) && a[j+1] == '=' {
				g.options = append(g.options, dockerRunOption{name: name, value: a[j+2:], hasValue: true})
				break
			}
			g.options = append(g.options, dockerRunOption{name: name})
		}
		groups = append(groups, g)
	}

	var line dockerRunLine
	for _, g := range groups {
		rewrite := false
		for _, o := range g.options {
			if err := refuseDockerRunOption(o); err != nil {
				return dockerRunLine{}, err
			}
			if t := dockerMountTarget(o); t != "" {
				line.targets = append(line.targets, t)
			}
			if o.name == "detach" {
				line.detach = line.detach || !optionOff(o)
			}
			rewrite = rewrite || dockerSidecarOptions[o.name] || o.name == "detach"
		}
		if !rewrite {
			line.user = append(line.user, g.raw...)
			continue
		}
		for _, o := range g.options {
			switch {
			case o.name == "detach":
			case dockerSidecarOptions[o.name]:
				line.sidecar = append(line.sidecar, o.tokens()...)
			default:
				line.user = append(line.user, o.tokens()...)
			}
		}
	}
	line.user = append(line.user, rest...)
	return line, nil
}

// refuseDockerRunOption names the options that cannot be honoured, before
// anything is started: what they ask for and what to do instead.
func refuseDockerRunOption(o dockerRunOption) error {
	switch o.name {
	case "publish-all":
		if optionOff(o) {
			return nil
		}
		return fmt.Errorf("--dockerrun: -P publishes the ports the image exposes, and the network is the\n" +
			"      sidecar's, whose image is not yours. Name them: -p 8080:8080")
	case "dns", "dns-search", "dns-option":
		return fmt.Errorf("--dockerrun: your `docker run` sets --%s, and the container takes its resolver\n"+
			"      from the cluster instead. Drop it: names resolve through plug, and anything else\n"+
			"      through the resolver it captured", o.name)
	case "network":
		v := o.value
		if v == "host" || v == "none" || strings.HasPrefix(v, "container:") || strings.HasPrefix(v, "service:") {
			return fmt.Errorf("--dockerrun: --network %s cannot carry the tunnel. The container shares the\n"+
				"      network of the sidecar that holds it, which needs a network of its own: a docker\n"+
				"      network the sidecar joins (--network my-net), or none at all", v)
		}
	}
	return nil
}

// optionOff reports a boolean option written off: --detach=false.
func optionOff(o dockerRunOption) bool {
	return o.hasValue && (o.value == "false" || o.value == "0")
}

// dockerMountTarget is the path in the container a mount option takes, "" for
// any other option. -v src:dst[:mode] or -v dst, a Windows source included
// (C:\data:/data): the target is the last part that is an absolute path.
// --mount names it target=, dst= or destination=; --tmpfs leads with it.
func dockerMountTarget(o dockerRunOption) string {
	if !o.hasValue {
		return ""
	}
	var t string
	switch o.name {
	case "volume":
		for _, p := range strings.Split(o.value, ":") {
			if strings.HasPrefix(p, "/") {
				t = p
			}
		}
	case "mount":
		for _, kv := range strings.Split(o.value, ",") {
			k, v, _ := strings.Cut(kv, "=")
			if k == "target" || k == "dst" || k == "destination" {
				t = v
			}
		}
	case "tmpfs":
		t, _, _ = strings.Cut(o.value, ":")
	}
	if !strings.HasPrefix(t, "/") {
		return ""
	}
	return path.Clean(t)
}

// mountedByUser reports whether the user's own mounts take the container path
// p: at it, or above it. The user's mount is the one the container gets, and
// what plug would have put there or under it is left out.
func mountedByUser(p string, targets []string) bool {
	p = path.Clean(p)
	for _, t := range targets {
		if p == t || t == "/" || strings.HasPrefix(p, t+"/") {
			return true
		}
	}
	return false
}
