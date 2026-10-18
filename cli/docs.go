package main

import "strings"

// Where the documentation lives — THE one place to change when the domain moves
// or the site is restructured. Every message that sends a reader to a page
// builds its link from here, and docs_test.go refuses a hard-coded one anywhere
// else in the package.
//
// The trailing slash belongs to the base: the site is published at the root of
// its own domain.
const docsBase = "https://plug.softwarity.dev/"

// The pages, named after what the reader is looking for rather than after the
// component that happens to render them today. Values are the router paths in
// docs/src/app/app.routes.ts.
const (
	docHome            = ""
	docKubernetes      = "kubernetes"
	docSwarm           = "swarm"
	docSecurity        = "security"
	docTroubleshooting = "troubleshooting"
	docProfiles        = "profiles"
	// Why a name served with -s can stop pointing at this machine: a CD
	// controller (Argo CD, Flux, Fleet, Crossplane) reverting the takeover.
	docContinuousDeployment = "continuous-deployment"
)

// In-page anchors, matching an id= in the page's template. Kept next to the
// pages so a renamed section is one edit, not a search across error strings.
// (None needed yet — every page linked from a message is the whole answer.)

// docURL renders the link to a page, optionally to a section within it.
//
// Every page is rendered at build time as <page>/index.html, so the address
// that answers directly is /kubernetes/: without the slash, Pages answers with
// a redirect to it. The slash also makes the link the page's canonical URL.
func docURL(page string, anchor ...string) string {
	var b strings.Builder
	b.WriteString(docsBase)
	if page != "" {
		b.WriteString(page)
		b.WriteString("/")
	}
	if len(anchor) > 0 && anchor[0] != "" {
		b.WriteString("#")
		b.WriteString(anchor[0])
	}
	return b.String()
}
