package agent

import "strings"

// Where the documentation lives, agent side. The CLI has its own copy in
// cli/docs.go: they are separate Go MODULES, so there is no shared package to
// put this in — a change of domain is two edits, both of them here and there,
// and docs_test.go in each module refuses a link written anywhere else.
//
// The trailing slash belongs to the base: the site is published at the root of
// its own domain.
const docsBase = "https://plug.softwarity.dev/"

// The pages, named after what the reader is looking for. Values are the router
// paths in docs/src/app/app.routes.ts. Only the home page is linked from the
// agent today (Preflight); a verb that points at another page adds its
// constant here, beside the CLI's copy.
const docHome = ""

// docURL renders the link to a page, optionally to a section within it. Each
// page is rendered at build time as <page>/index.html, so the link carries the
// slash Pages would otherwise redirect to; see cli/docs.go.
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
