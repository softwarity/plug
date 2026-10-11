package main

// Which flavour a build, or a cluster, is.
//
// plug is distributed BY the cluster it serves: `ssh get@<agent> install | sh`
// hands out the binaries baked into that agent's image. So a gateway that embeds
// the agent (Meerkat) ships its own client, stamped hosted, and its agent
// announces a hosted version.
//
// The commands are the same in both. What differs is decided PER CLUSTER, from
// the version its agent announces, never from where the binary on this machine
// came from: one plug in a PATH serves every profile, and a machine with a
// gateway's cluster and a standalone one has profiles of both kinds. Deciding by
// the binary refused `plug -p <standalone> update` to whoever had last installed
// from a gateway. So `plug update` refuses a gateway's cluster, after asking its
// agent, and runs everywhere else.

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

// hostedVersion reports whether v, a version a build is stamped with or an
// agent announces, is a gateway's.
func hostedVersion(v string) bool { return strings.HasSuffix(v, "-"+hostedFlavour) }

// gatewayDecides is the reason a gateway's cluster gives for the update
// commands. One line, naming the reason rather than the mechanism.
const gatewayDecides = "the gateway that serves this cluster decides its version, and its agent's"

// usageFor is one section of the help. Every build lists every section: the
// commands are the same in both flavours (see the top of this file).
func usageFor(section string) string {
	switch section {
	case "update":
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
		return `  plug versions                        launcher, cached cores, and the agent
                                       version of every profile
`
	case "keys":
		return `  plug keygen [-p profile] [--renew]   give this profile its own key pair, kept
                                       in ~/.plug/keys/. Enrol the public half
                                       with whoever operates the cluster
  plug pubkey [-p profile]             print that profile's public key, ready to
                                       paste where the operator enrols it
`
	}
	return ""
}
