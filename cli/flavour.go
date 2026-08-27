package main

// Which commands this build carries.
//
// plug is distributed BY the cluster it serves: `ssh get@<agent> install | sh`
// hands out the binaries baked into that agent's image. So a gateway that embeds
// the agent (Meerkat) ships its own client, and some verbs make no sense against
// it while others only make sense there.
//
// A verb that does not apply is ABSENT: missing from the help and refused on
// execution. Not disabled by a flag, because a flag has to be known in advance
// and is discovered in production by the person who did not know.
//
// The trade this accepts, stated because it is real: the verb set follows WHERE
// THE BINARY CAME FROM, not which cluster it is talking to. There is one plug in
// a PATH, and its identity is decided at install. Someone who installed from a
// gateway and later reaches a standalone cluster still has no `update`. The
// alternative would be to decide per call, from what the agent announces, which
// keeps every verb present everywhere; this is the deliberate other choice.

import "strings"

// The flavour rides in the VERSION STRING and nowhere else, so there is nothing
// to keep in sync. The build stamps one thing:
//
//	-ldflags "-X main.version=2.12.0-hosted"
//
// That string is already the cache key for the core the launcher fetches
// (ensureVersion) and the value the agent announces, so carrying the flavour
// there is what keeps two flavours from colliding on one cached binary. A second
// stamped constant could disagree with it; a derived one cannot.
const hostedFlavour = "hosted"

// hosted reports whether this build serves an embedded agent.
func hosted() bool { return strings.HasSuffix(version, "-"+hostedFlavour) }

// Verbs that exist in one flavour only. Everything not named here is common:
// init, ls, rm, rn, config, test, doctor, prune, down, uninstall, version,
// about, selftest, install-service, remove-service.
var (
	// standaloneOnly: the cluster is where the truth about versions lives, and a
	// gateway serves its own client. Asking the public release what to run could
	// move a developer onto a build their cluster does not serve.
	standaloneOnly = map[string]string{
		"update":   "the gateway that serves this plug decides its version",
		"versions": "the list that counts is the one the gateway serves",
	}
	// hostedOnly: a personal key needs somebody on the other side to enrol and
	// check it. Standalone, the key built into every published binary proves you
	// have plug and nothing more, which is the assumed model there.
	hostedOnly = map[string]string{
		"keygen": "this cluster does not check personal keys",
		"pubkey": "this cluster does not check personal keys",
	}
)

// verbAvailable reports whether this build answers to name, and why not when it
// does not. A verb of the other flavour is not an unknown word: saying so beats
// falling through to launcherRun, which would try to run `update` as a program
// inside the cluster and fail on something unrelated.
func verbAvailable(name string) (why string, ok bool) {
	if hosted() {
		if why, isOther := standaloneOnly[name]; isOther {
			return why, false
		}
		return "", true
	}
	if why, isOther := hostedOnly[name]; isOther {
		return why, false
	}
	return "", true
}

// refuseVerb ends the run on a verb this build does not carry. One line, naming
// the reason rather than the mechanism: nobody needs to hear about build flags.
func refuseVerb(name, why string) {
	fatal("plug %s is not part of this install: %s.\n"+
		"      This plug was installed from the cluster it serves, and carries what applies there.",
		name, why)
}

// usageFor keeps the help honest: a section whose verbs this build does not
// carry is not printed. A help that lists a command which then refuses is worse
// than no help at all.
func usageFor(section string) string {
	switch section {
	case "update":
		if hosted() {
			return ""
		}
		return `  plug update [-p profile] [<tag>]     update that cluster's agent (it refreshes
                                       its own deployment from the registry),
                                       then this launcher from the agent.
                                       A tag SWITCHES the channel it follows:
                                         plug update tag       newest release
                                         plug update latest    the latest stream
                                         plug update feat-09   that branch's tag
                                       The agent checks the tag exists first.
`
	case "versions":
		if hosted() {
			return ""
		}
		return `  plug versions                        launcher, cached cores, and the agent
                                       version of every profile
`
	case "keys":
		if !hosted() {
			return ""
		}
		return `  plug keygen [-p profile] [--renew]   give this profile its own key pair, kept
                                       in ~/.plug/keys/. Enrol the public half
                                       with whoever operates the cluster
  plug pubkey [-p profile]             print that profile's public key, ready to
                                       paste where the operator enrols it
`
	}
	return ""
}
