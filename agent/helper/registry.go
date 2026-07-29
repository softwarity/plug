package main

// Finding the newest RELEASE on the registry that holds this agent's own image.
//
// plug's distribution point is the agent image, and plug is infrastructure, not
// an application dependency: there is no reproducibility argument for holding a
// cluster on an old agent. So `plug update` MOVES the deployment's tag — a stack
// pinned to softwarity/plug:2.3.0 is repointed at :2.4.0 — rather than merely
// re-resolving whatever tag it already carries (which, pinned, can only ever
// return the same image).
//
// Two kinds of tag, two behaviours:
//
//   - A RELEASE tag (2, 2.3, 2.3.0) is retargeted at the newest x.y.z published
//     for that repository, major versions included.
//   - A MOVING tag (latest, main, a branch name) is left alone and simply
//     re-pulled: it already resolves to whatever its publisher last pushed, and
//     repointing it would override a deliberate choice.
//
// The lookup goes to the registry that actually holds the image, not to Docker
// Hub by name — a mirror or a private registry is the one to ask about its own
// tags, and it is also the only one the agent is guaranteed to reach.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// readAllLimited drains r up to max bytes — a registry is a remote we do not
// control, and a listing is small; nothing justifies an unbounded read.
func readAllLimited(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max))
}

// releaseTagRe matches a tag that names a release rather than a stream: 2,
// 2.3, 2.3.0 (a leading v tolerated). Anything else — latest, main, a branch —
// is a moving tag and is never retargeted.
var releaseTagRe = regexp.MustCompile(`^v?\d+(\.\d+){0,2}$`)

// exactReleaseRe matches the tags worth retargeting TO: a full x.y.z. The
// shorter forms (2, 2.3) are moving tags of their own, so aiming at one would
// trade a pin for a pin that lies.
var exactReleaseRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// isReleaseTag reports whether the deployment pinned a release.
func isReleaseTag(tag string) bool { return releaseTagRe.MatchString(tag) }

// How an update should proceed for one deployed image.
const (
	planRetarget = "retarget" // a release tag: move it to a newer release
	planResolve  = "resolve"  // a moving tag: leave it, re-resolve/pull it
	planCurrent  = "current"  // a release tag, already the newest: nothing to do
)

// retarget decides what to do with the image a deployment carries, asking the
// registry when the tag is a pinned release.
func retarget(img string) (target, plan, note string) {
	var newest string
	var err error
	if _, _, tag := parseImageRef(img); isReleaseTag(tag) {
		newest, err = newestRelease(img)
	}
	return retargetPlan(img, localVersion(), newest, err)
}

// retargetPlan is that decision on its own, with the registry's answer passed
// in — so every branch is exercisable without a network. Returns the image to
// deploy, one of the plan* constants, and a sentence for the reply.
//
// A registry that cannot be listed is NOT a failure: the update degrades to
// re-resolving the current tag — the behaviour before this existed — and says
// why. Doing less is better than refusing to update because a listing failed.
func retargetPlan(img, running, newest string, listErr error) (target, plan, note string) {
	_, _, tag := parseImageRef(img)
	if !isReleaseTag(tag) {
		return img, planResolve, fmt.Sprintf("re-resolving the moving tag %s", img)
	}
	if listErr != nil {
		return img, planResolve, fmt.Sprintf("re-resolving %s — could not list the registry's releases (%v)", img, listErr)
	}
	if !releaseNewerThan(newest, running) {
		return img, planCurrent, fmt.Sprintf("v%s — already the newest release published for %s", running, img)
	}
	t := retagged(img, newest)
	return t, planRetarget, fmt.Sprintf("moving the deployment from %s to %s", img, t)
}

// wantNewestRelease is the tag the operator asks for to mean "the newest
// release", as opposed to naming one stream (latest, a branch) or one exact
// version. It is a word, not a tag, because the newest release is a moving
// answer the registry has to be asked for.
const wantNewestRelease = "tag"

// retargetTo is retarget when the operator NAMED where to go — `plug update
// latest`, `plug update tag`, `plug update feat-09`. That is a channel switch,
// not "is there something newer on the channel I am on", so the two questions
// are answered by different code:
//
//   - retarget follows the deployment's own tag and only ever moves along it;
//   - this one changes which tag the deployment carries, and the version that
//     comes with it is whatever that tag currently points at — up or down.
//
// The tag is checked against the registry first. Repointing a deployment at a
// tag that does not exist replaces a working agent with one that cannot pull,
// and on Swarm/k8s that is a rollout you then have to unwind by hand.
func retargetTo(img, want string) (target, plan, note string) {
	host, repo, _ := parseImageRef(img)
	tags, err := registryTags(host, repo)
	return retargetToWith(img, want, tags, err)
}

// retargetToWith is that decision on its own, with the registry's listing
// passed in — so every branch is exercisable without a network. An empty plan
// means REFUSED, and note says why.
func retargetToWith(img, want string, tags []string, listErr error) (target, plan, note string) {
	_, repo, cur := parseImageRef(img)
	if listErr != nil {
		// Unlike retarget, there is no safe degradation here: the whole request
		// is "move me to that tag", and moving without checking is the failure
		// mode this function exists to avoid.
		return "", "", fmt.Sprintf("cannot list the tags of %s (%v)", repo, listErr)
	}
	tag := want
	if want == wantNewestRelease {
		best, bestV := "", [3]int{-1, 0, 0}
		for _, t := range tags {
			v, ok := parseExactRelease(t)
			if !ok {
				continue
			}
			if versionLess(bestV, v) {
				best, bestV = t, v
			}
		}
		if best == "" {
			return "", "", fmt.Sprintf("no x.y.z release among the %d tags of %s", len(tags), repo)
		}
		tag = best
	} else if !slices.Contains(tags, want) {
		return "", "", fmt.Sprintf("%s has no tag %q — published: %s", repo, want, tagHint(tags))
	}
	t := retagged(img, tag)
	if tag == cur {
		// Already carrying it. A release pin cannot move, so there is nothing to
		// do; a moving tag may have been rebuilt since, so re-resolve it.
		if isReleaseTag(tag) {
			return t, planCurrent, fmt.Sprintf("already on %s", t)
		}
		return t, planResolve, fmt.Sprintf("re-resolving %s", t)
	}
	return t, planRetarget, fmt.Sprintf("switching the deployment from %s to %s", img, t)
}

// retargetImageOnly is retarget's first return, for messages that only need to
// name the image an operator should deploy by hand.
func retargetImageOnly(img string) string {
	t, _, _ := retarget(img)
	return t
}

// parseImageRef splits name[:tag] into the registry API host, the repository
// path as the v2 API wants it, and the tag. It implements the same defaulting
// docker does: a first component with a dot, a colon or "localhost" is a
// registry host, otherwise the ref belongs to Docker Hub — where a single-part
// name lives under library/.
func parseImageRef(ref string) (host, repo, tag string) {
	// A digest comes after the tag and carries its own colon — strip it first,
	// or "plug:2.3.0@sha256:abc" would parse its tag as "abc".
	if i := strings.Index(ref, "@"); i > 0 {
		ref = ref[:i]
	}
	tag = "latest"
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref, tag = ref[:i], ref[i+1:]
	}
	host = "registry-1.docker.io"
	repo = ref
	if i := strings.Index(ref, "/"); i > 0 {
		first := ref[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			host, repo = first, ref[i+1:]
			if host == "docker.io" || host == "index.docker.io" {
				host = "registry-1.docker.io"
			}
		}
	}
	if host == "registry-1.docker.io" && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return host, repo, tag
}

// retagged rebuilds an image reference with a different tag, dropping any
// pinned digest (a digest and a new tag contradict each other, and a stack
// deploy pins one).
func retagged(ref, tag string) string {
	if i := strings.Index(ref, "@sha256:"); i > 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref + ":" + tag
}

// newestRelease returns the highest x.y.z tag published for ref's repository.
// Errors are the caller's cue to fall back to re-resolving the current tag:
// a registry that cannot be listed (auth, a proxy, an old registry) must
// degrade to the previous behaviour, never block the update.
func newestRelease(ref string) (string, error) {
	host, repo, _ := parseImageRef(ref)
	tags, err := registryTags(host, repo)
	if err != nil {
		return "", err
	}
	best, bestV := "", [3]int{-1, 0, 0}
	for _, t := range tags {
		v, ok := parseExactRelease(t)
		if !ok {
			continue
		}
		if versionLess(bestV, v) {
			best, bestV = t, v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no x.y.z tag among the %d tags of %s", len(tags), repo)
	}
	return best, nil
}

func parseExactRelease(tag string) ([3]int, bool) {
	m := exactReleaseRe.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// releaseNewerThan reports whether tag is a strictly newer release than the
// version this agent runs. Guards the retarget against going backwards — a
// registry whose newest tag is older than what is deployed (tags pruned, a
// mirror lagging) must not be allowed to downgrade the cluster.
func releaseNewerThan(tag, current string) bool {
	tv, ok := parseExactRelease(tag)
	if !ok {
		return false
	}
	// The running version carries +<rev> build metadata; drop it.
	cur, _, _ := strings.Cut(current, "+")
	cv, ok := parseExactRelease(cur)
	if !ok {
		return true // a dev agent: any release is a step forward
	}
	return versionLess(cv, tv)
}

// registryTags lists a repository's tags over the v2 API, answering the Bearer
// challenge anonymously when the registry issues one (what Docker Hub does for
// public repositories). Paginated registries are followed through their Link
// header.
func registryTags(host, repo string) ([]string, error) {
	cl := &http.Client{Timeout: 30 * time.Second}
	next := "https://" + host + "/v2/" + repo + "/tags/list?n=100"
	var token string
	var all []string
	for page := 0; next != "" && page < 50; page++ {
		body, link, err := registryGET(cl, next, &token)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("listing %s tags: %v", repo, err)
		}
		all = append(all, payload.Tags...)
		next = ""
		if link != "" {
			if u := parseNextLink(link); u != "" {
				next = "https://" + host + u
			}
		}
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%s has no tags (or the registry did not list them)", repo)
	}
	return all, nil
}

// registryGET performs one listing request, obtaining a token on the first 401
// and retrying once. token is carried across pages so the challenge is answered
// at most once.
func registryGET(cl *http.Client, endpoint string, token *string) (body []byte, link string, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest("GET", endpoint, nil)
		if err != nil {
			return nil, "", err
		}
		if *token != "" {
			req.Header.Set("Authorization", "Bearer "+*token)
		}
		resp, err := cl.Do(req)
		if err != nil {
			return nil, "", err
		}
		data, readErr := readAllLimited(resp.Body, 4<<20)
		resp.Body.Close()
		if readErr != nil {
			return nil, "", readErr
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			t, terr := registryToken(cl, resp.Header.Get("Www-Authenticate"))
			if terr != nil {
				return nil, "", terr
			}
			*token = t
			continue
		}
		if resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("registry answered %s", resp.Status)
		}
		return data, resp.Header.Get("Link"), nil
	}
	return nil, "", fmt.Errorf("registry kept refusing the tag listing")
}

// registryToken answers a Bearer challenge anonymously:
// Www-Authenticate: Bearer realm="…",service="…",scope="…"
func registryToken(cl *http.Client, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("the registry requires credentials this agent does not hold")
	}
	params := map[string]string{}
	for _, part := range splitChallenge(challenge[len("Bearer "):]) {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		params[strings.ToLower(k)] = strings.Trim(v, `"`)
	}
	realm := params["realm"]
	if realm == "" {
		return "", fmt.Errorf("the registry's auth challenge carried no realm")
	}
	q := url.Values{}
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	if params["scope"] != "" {
		q.Set("scope", params["scope"])
	}
	resp, err := cl.Get(realm + "?" + q.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("auth server answered %s", resp.Status)
	}
	data, err := readAllLimited(resp.Body, 1<<20)
	if err != nil {
		return "", err
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &tok); err != nil {
		return "", err
	}
	if t := firstNonEmpty(tok.Token, tok.AccessToken); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("auth server returned no token")
}

// splitChallenge splits a Www-Authenticate parameter list on commas that are
// NOT inside a quoted value (a scope legitimately contains commas:
// scope="repository:x/y:pull,push").
func splitChallenge(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ',' && !quoted:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// parseNextLink extracts the URL of a rel="next" Link header.
func parseNextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		seg := strings.Split(strings.TrimSpace(part), ";")
		if len(seg) < 2 || !strings.Contains(seg[1], `rel="next"`) {
			continue
		}
		return strings.Trim(strings.TrimSpace(seg[0]), "<>")
	}
	return ""
}

// updatePlan is the single entry point the backends use: follow the tag the
// deployment carries, or move to the one the operator named. A named target
// that cannot be resolved is fatal here — the caller is being asked to repoint
// a live deployment, and "I could not check" is not a reason to do it anyway.
func updatePlan(img, want string) (target, plan, note string) {
	if want == "" {
		return retarget(img)
	}
	target, plan, note = retargetTo(img, want)
	if plan == "" {
		answer("error: %s", note)
	}
	return target, plan, note
}

// tagHint names the tags worth showing when someone mistypes one: every stream
// (latest, main, a branch) plus the newest few releases. A repository accretes
// one release tag per version forever — printing all of them buries the two or
// three lines that answer "what could I have meant?".
func tagHint(tags []string) string {
	var streams, releases []string
	for _, t := range tags {
		if isReleaseTag(t) {
			releases = append(releases, t)
		} else {
			streams = append(streams, t)
		}
	}
	slices.SortFunc(releases, func(a, b string) int {
		av, _ := parseExactRelease(a)
		bv, _ := parseExactRelease(b)
		switch {
		case versionLess(av, bv):
			return 1
		case versionLess(bv, av):
			return -1
		}
		return 0
	})
	const keep = 4
	more := ""
	if len(releases) > keep {
		more = fmt.Sprintf(" (+%d older)", len(releases)-keep)
		releases = releases[:keep]
	}
	return strings.Join(append(streams, releases...), ", ") + more
}
