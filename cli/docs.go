package main

import "strings"

// Where the documentation lives — THE one place to change when the domain moves
// or the site is restructured. Every message that sends a reader to a page
// builds its link from here, and docs_test.go refuses a hard-coded one anywhere
// else in the package.
//
// The trailing slash belongs to the base: the site is published under a path
// (/plug/), not at the root of its host.
const docsBase = "https://softwarity.github.io/plug/"

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
)

// In-page anchors, matching an id= in the page's template. Kept next to the
// pages so a renamed section is one edit, not a search across error strings.
const (
	anchorGitOps = "gitops"
)

// docURL renders the link to a page, optionally to a section within it.
//
// Deep links work because the deploy copies index.html to 404.html — Pages has
// no file at /plug/kubernetes and would otherwise serve its own 404. If that
// step ever disappears, every link built here silently stops resolving, which
// is why it is called out in .github/workflows/deploy-doc.yml.
func docURL(page string, anchor ...string) string {
	var b strings.Builder
	b.WriteString(docsBase)
	b.WriteString(page)
	if len(anchor) > 0 && anchor[0] != "" {
		b.WriteString("#")
		b.WriteString(anchor[0])
	}
	return b.String()
}
