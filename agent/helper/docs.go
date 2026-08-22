package main

import "strings"

// Where the documentation lives, agent side. The CLI has its own copy in
// cli/docs.go: they are separate Go MODULES, so there is no shared package to
// put this in — a change of domain is two edits, both of them here and there,
// and docs_test.go in each module refuses a link written anywhere else.
//
// The trailing slash belongs to the base: the site is published under a path
// (/plug/), not at the root of its host.
const docsBase = "https://softwarity.github.io/plug/"

// The pages, named after what the reader is looking for. Values are the router
// paths in docs/src/app/app.routes.ts.
const (
	docHome       = ""
	docKubernetes = "kubernetes"
	docSwarm      = "swarm"
)

// In-page anchors, matching an id= in the page's template.
const (
	anchorGitOps = "gitops"
)

// docURL renders the link to a page, optionally to a section within it. Deep
// links resolve because the deploy copies index.html to 404.html — see
// .github/workflows/deploy-doc.yml.
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
