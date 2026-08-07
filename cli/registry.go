package main

// Client-side registry lookup for `plug update`: the CLI asks the registry the
// agent's image lives in, decides the target, and hands the agent an already
// checked tag to APPLY (`self-update apply <tag>`).
//
// Why from HERE: the agent's own lookup leaves the cluster through the Docker
// Desktop VM, whose DNS follows the workstation's — which a plug session has
// plugged. Measured on such a workstation: ~31s per registry round-trip from
// the VM, ~1s for the same listing from this process (dotted names ride the
// stub straight to the saved upstream, not the tunnel). The agent-side path
// remains as the fallback for a registry only the cluster can reach.
//
// The pure helpers are MIRRORED from agent/helper/registry.go (separate Go
// modules) — keep changes in sync with their twin.

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

// releaseTagRe matches a tag that names a release rather than a stream: 2,
// 2.3, 2.3.0 (a leading v tolerated). Anything else — latest, main, a branch —
// is a moving tag.
var releaseTagRe = regexp.MustCompile(`^v?\d+(\.\d+){0,2}$`)

// exactReleaseRe matches the tags worth retargeting TO: a full x.y.z.
var exactReleaseRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func isReleaseTag(tag string) bool { return releaseTagRe.MatchString(tag) }

func parseExactRelease(tag string) ([3]int, bool) {
	m := exactReleaseRe.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func versionLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func releaseNewerThan(tag, current string) bool {
	tv, ok := parseExactRelease(tag)
	if !ok {
		return false
	}
	cv, ok := parseExactRelease(runningRelease(current))
	if !ok {
		return false
	}
	return versionLess(cv, tv)
}

// runningRelease strips the build metadata an agent's version may carry —
// releases before 2.4.1 were stamped `x.y.z+<rev>`. Without this, such an agent
// reads as unparseable and every lookup is delegated to it, which is exactly
// the slow path this file exists to avoid. Mirrors the agent twin.
func runningRelease(v string) string {
	base, _, _ := strings.Cut(v, "+")
	return base
}

// newestOf picks the newest x.y.z among tags, or "" when none is published.
func newestOf(tags []string) string {
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
	return best
}

// parseImageRef splits name[:tag] into the registry API host, the repository
// path as the v2 API wants it, and the tag — the same defaulting docker does.
func parseImageRef(ref string) (host, repo, tag string) {
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
	if !strings.Contains(repo, "/") && host == "registry-1.docker.io" {
		repo = "library/" + repo
	}
	return host, repo, tag
}

// imageHasTag reports whether ref carries an explicit tag (digest excluded).
func imageHasTag(ref string) bool {
	if i := strings.Index(ref, "@"); i > 0 {
		ref = ref[:i]
	}
	return strings.LastIndex(ref, ":") > strings.LastIndex(ref, "/")
}

// registryTags lists the repository's tags from the registry that holds it,
// token auth and Link pagination included.
func registryTags(host, repo string) ([]string, error) {
	return registryTagsWithin(host, repo, 4*time.Second)
}

// registryTagsWithin is registryTags with an explicit budget.
//
// `plug update` uses 4s, aggressively short on purpose: there the lookup is an
// OPTIMIZATION with a full fallback behind it (the agent does its own), and on
// the machines it helps least — a workstation whose per-process routing or VPN
// filters some paths — a generous budget just delays the fallback. Measured
// healthy: ~1s.
//
// The background check has NO fallback: a timeout there is not a slower path,
// it is no check at all, silently. And nobody is waiting on it. So it asks for
// more room.
func registryTagsWithin(host, repo string, budget time.Duration) ([]string, error) {
	cl := &http.Client{Timeout: budget}
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
			return nil, err
		}
		all = append(all, payload.Tags...)
		if link = parseNextLink(link); link != "" && !strings.HasPrefix(link, "http") {
			link = "https://" + host + link
		}
		next = link
	}
	return all, nil
}

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
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			challenge := resp.Header.Get("WWW-Authenticate")
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			t, terr := registryToken(cl, challenge)
			if terr != nil {
				return nil, "", terr
			}
			*token = t
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, "", rerr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("%s: %s", endpoint, resp.Status)
		}
		return b, resp.Header.Get("Link"), nil
	}
	return nil, "", fmt.Errorf("%s: unauthorized", endpoint)
}

// registryToken follows a Bearer challenge (realm/service/scope) and returns
// the token — the Docker Hub anonymous-pull flow.
func registryToken(cl *http.Client, challenge string) (string, error) {
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("unsupported auth challenge %q", challenge)
	}
	var realm string
	params := url.Values{}
	for _, part := range splitChallenge(strings.TrimPrefix(challenge, "Bearer ")) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		if k == "realm" {
			realm = v
		} else {
			params.Add(k, v)
		}
	}
	if realm == "" {
		return "", fmt.Errorf("auth challenge without a realm: %q", challenge)
	}
	resp, err := cl.Get(realm + "?" + params.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return "", err
	}
	if payload.Token != "" {
		return payload.Token, nil
	}
	return payload.AccessToken, nil
}

func splitChallenge(s string) []string {
	var parts []string
	depth := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			depth = !depth
		case ',':
			if !depth {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(s[start:]))
	return parts
}

func parseNextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		if i, j := strings.Index(part, "<"), strings.Index(part, ">"); i >= 0 && j > i {
			return part[i+1 : j]
		}
	}
	return ""
}

// tagHint names the tags worth showing when someone mistypes one: every stream
// plus the newest few releases.
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

// decideClient turns the listing into ONE of four outcomes for `plug update`:
// a tag to hand the agent for a checked APPLY, a "current" verdict that ends
// the command without touching the cluster at all, a fatal message, or a
// delegation to the agent's own lookup (moving tags, dev builds — cases whose
// truth lives cluster-side).
func decideClient(img, before, want string, tags []string) (apply, current, errMsg string, delegate bool) {
	_, repo, cur := parseImageRef(img)
	switch want {
	case "":
		// Follow the deployment's channel. Only a release pin (tag or digest)
		// answers "is there something newer" from a listing; a moving tag's
		// currentness is a digest question only the cluster can answer.
		if imageHasTag(img) && !isReleaseTag(cur) {
			return "", "", "", true
		}
		if _, ok := parseExactRelease(runningRelease(before)); !ok {
			return "", "", "", true // a dev/unversioned agent: let it decide
		}
		newest := newestOf(tags)
		if newest == "" {
			return "", "", "", true
		}
		if !releaseNewerThan(newest, before) {
			return "", fmt.Sprintf("v%s — already the newest release published for %s (checked from this machine)", runningRelease(before), img), "", false
		}
		return newest, "", "", false
	case wantNewestReleaseCLI:
		newest := newestOf(tags)
		if newest == "" {
			return "", "", fmt.Sprintf("no x.y.z release among the %d tags of %s", len(tags), repo), false
		}
		return newest, "", "", false
	default:
		if !slices.Contains(tags, want) {
			return "", "", fmt.Sprintf("%s has no tag %q — published: %s", repo, want, tagHint(tags)), false
		}
		return want, "", "", false
	}
}

// wantNewestReleaseCLI mirrors the agent's `tag` keyword: the newest release,
// as opposed to naming one stream or one exact version.
const wantNewestReleaseCLI = "tag"

// imageDigest returns the digest a reference pins, or "" when it names none.
// The agent's `info` reports its running image WITH the digest it resolved to
// (softwarity/plug:latest@sha256:…), which is what makes a moving tag decidable
// from here at all.
func imageDigest(ref string) string {
	if i := strings.Index(ref, "@"); i > 0 {
		return ref[i+1:]
	}
	return ""
}

// movingTagOf returns the tag a reference follows when that tag MOVES (latest,
// main, a branch) — "" for a release pin, which a tag listing already answers,
// and "" for a bare digest, which by definition cannot move.
func movingTagOf(ref string) string {
	if !imageHasTag(ref) {
		return ""
	}
	_, _, tag := parseImageRef(ref)
	if isReleaseTag(tag) {
		return ""
	}
	return tag
}

// registryDigestWithin resolves what a tag points to RIGHT NOW — the manifest
// digest, from the registry's own header.
//
// This is the question a tag listing cannot answer. "Is there a newer release?"
// is settled by comparing version numbers; "has `latest` moved?" has no version
// to compare, only bytes. One request, so a short budget is plenty.
//
// Accept lists both manifest-list and single-manifest types: asking for only one
// makes a registry answer with a DIFFERENT digest (the one it converted to),
// which would read as a move on every single check.
func registryDigestWithin(host, repo, tag string, budget time.Duration) (string, error) {
	cl := &http.Client{Timeout: budget}
	endpoint := "https://" + host + "/v2/" + repo + "/manifests/" + tag
	accept := strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", ")
	var token string
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest("HEAD", endpoint, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", accept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := cl.Do(req)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			t, terr := registryToken(cl, resp.Header.Get("WWW-Authenticate"))
			if terr != nil {
				return "", terr
			}
			token = t
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s: %s", endpoint, resp.Status)
		}
		d := resp.Header.Get("Docker-Content-Digest")
		if d == "" {
			return "", fmt.Errorf("%s: no Docker-Content-Digest header", endpoint)
		}
		return d, nil
	}
	return "", fmt.Errorf("%s: unauthorized", endpoint)
}
