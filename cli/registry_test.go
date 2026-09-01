package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// decideClient is the whole client-side lookup in one pure function; these are
// the outcomes `plug update` can reach without touching the cluster.
func TestDecideClient(t *testing.T) {
	tags := []string{"latest", "main", "2.5.2", "2.5.1", "2.4.0"}
	for _, c := range []struct {
		name, img, before, want string
		apply, current, errSub  string
		delegate                bool
	}{
		{"release pin, newer published", "softwarity/plug:2.5.1", "2.5.1", "", "2.5.2", "", "", false},
		{"release pin, already newest", "softwarity/plug:2.5.2", "2.5.2", "", "", "already the newest", "", false},
		{"digest-only pin follows releases", "softwarity/plug@sha256:abc", "2.5.1", "", "2.5.2", "", "", false},
		{"moving tag: cluster's call", "softwarity/plug:latest", "2.5.2", "", "", "", "", true},
		{"dev agent: cluster's call", "softwarity/plug:2.5.1", "dev", "", "", "", "", true},
		{"want tag → newest release", "softwarity/plug:latest", "2.5.2", "tag", "2.5.2", "", "", false},
		{"want a published stream", "softwarity/plug:2.5.2", "2.5.2", "main", "main", "", "", false},
		{"want an exact release", "softwarity/plug:2.5.2", "2.5.2", "2.4.0", "2.4.0", "", "", false},
		{"want an unpublished tag", "softwarity/plug:2.5.2", "2.5.2", "nope", "", "", "has no tag", false},
	} {
		apply, current, errMsg, delegate := decideClient(c.img, c.before, c.want, tags)
		if apply != c.apply || delegate != c.delegate ||
			(c.current == "") != (current == "") || (c.errSub == "") != (errMsg == "") {
			t.Errorf("%s: got (apply=%q current=%q err=%q delegate=%v)", c.name, apply, current, errMsg, delegate)
			continue
		}
		if c.current != "" && !strings.Contains(current, c.current) {
			t.Errorf("%s: current %q lacks %q", c.name, current, c.current)
		}
		if c.errSub != "" && !strings.Contains(errMsg, c.errSub) {
			t.Errorf("%s: err %q lacks %q", c.name, errMsg, c.errSub)
		}
	}
}

// The mirrored parseImageRef must agree with docker's defaulting.
func TestParseImageRefCLI(t *testing.T) {
	for _, c := range []struct{ ref, host, repo, tag string }{
		{"softwarity/plug:2.5.2", "registry-1.docker.io", "softwarity/plug", "2.5.2"},
		{"softwarity/plug@sha256:abc", "registry-1.docker.io", "softwarity/plug", "latest"},
		{"docker.io/softwarity/plug:2.5.2", "registry-1.docker.io", "softwarity/plug", "2.5.2"},
		{"registry.corp:5000/team/plug:dev", "registry.corp:5000", "team/plug", "dev"},
		{"redis", "registry-1.docker.io", "library/redis", "latest"},
	} {
		h, r, tg := parseImageRef(c.ref)
		if h != c.host || r != c.repo || tg != c.tag {
			t.Errorf("parseImageRef(%q) = (%q,%q,%q), want (%q,%q,%q)", c.ref, h, r, tg, c.host, c.repo, c.tag)
		}
	}
}

// An agent stamped x.y.z+<rev> (every release before 2.4.1) must take the
// client-side fast path, not be mistaken for a dev build and delegated.
func TestDecideClientHandlesBuildMetadata(t *testing.T) {
	tags := []string{"latest", "2.5.4", "2.4.0"}
	apply, current, errMsg, delegate := decideClient("softwarity/plug:2.4.0", "2.4.0+983761c", "", tags)
	if delegate || errMsg != "" || current != "" || apply != "2.5.4" {
		t.Errorf("got (apply=%q current=%q err=%q delegate=%v), want apply=2.5.4", apply, current, errMsg, delegate)
	}
	// and when it is already the newest, say so instead of delegating
	_, current, _, delegate = decideClient("softwarity/plug:2.5.4", "2.5.4+abc1234", "", tags)
	if delegate || current == "" {
		t.Errorf("already-newest with +rev: current=%q delegate=%v", current, delegate)
	}
	if strings.Contains(current, "+abc1234") {
		t.Errorf("build metadata leaked into the message: %q", current)
	}
}

// registryToken had drifted away from the agent's twin, and every difference
// was the CLI being the weaker of the two: a registry the cluster can pull from
// while this machine cannot is the worst possible failure for a lookup whose
// only reason to exist is being faster than the cluster's.
//
// tokenServer answers a token request the way an auth server does, recording
// the query it was asked with.
func tokenServer(t *testing.T, status int, body string, seen *url.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// RFC 9110 section 11.1: the auth scheme is case-insensitive. A registry
// answering `bearer realm=...` was refused here and accepted by the agent, so
// the fast path failed and the same registry worked from the cluster.
func TestRegistryTokenAcceptsALowercaseScheme(t *testing.T) {
	srv := tokenServer(t, http.StatusOK, `{"token":"tok-lower"}`, nil)
	got, err := registryToken(srv.Client(), `bearer realm="`+srv.URL+`",service="reg"`)
	if err != nil || got != "tok-lower" {
		t.Fatalf("registryToken on a lowercase challenge = %q, %v; want the token", got, err)
	}
}

// An auth server that refuses says so with a status. Parsing its error page as
// a token document takes whatever happens to be in it, or nothing at all, and
// the refusal never reaches the caller.
func TestRegistryTokenRefusesAnErrorStatus(t *testing.T) {
	srv := tokenServer(t, http.StatusServiceUnavailable, `{"token":"from-an-error-page"}`, nil)
	got, err := registryToken(srv.Client(), `Bearer realm="`+srv.URL+`",service="reg"`)
	if err == nil {
		t.Fatalf("a 503 from the auth server must be an error, got token %q", got)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("the error must name the status the auth server answered, got %v", err)
	}
}

// A token document with no token used to come back as ("", nil), and the next
// request went out with a literal `Authorization: Bearer ` and no token behind
// it. The registry then answers 401 again and the report blames the listing.
func TestRegistryTokenRefusesAnEmptyToken(t *testing.T) {
	srv := tokenServer(t, http.StatusOK, `{"expires_in":300}`, nil)
	got, err := registryToken(srv.Client(), `Bearer realm="`+srv.URL+`",service="reg"`)
	if err == nil {
		t.Fatalf("an empty token must be an error, got %q", got)
	}
}

// Only service and scope belong in the token request. A 401 challenge carries
// the registry's own diagnostics too (RFC 6750 adds error and
// error_description); echoing them back is at best noise and at worst a 400
// from a token server that validates its query.
func TestRegistryTokenForwardsOnlyServiceAndScope(t *testing.T) {
	var seen url.Values
	srv := tokenServer(t, http.StatusOK, `{"access_token":"tok-access"}`, &seen)
	challenge := `Bearer realm="` + srv.URL + `",service="registry.docker.io",` +
		`scope="repository:softwarity/plug:pull,push",error="insufficient_scope"`
	got, err := registryToken(srv.Client(), challenge)
	if err != nil || got != "tok-access" {
		t.Fatalf("registryToken = %q, %v; want the access_token", got, err)
	}
	if seen.Get("service") != "registry.docker.io" {
		t.Errorf("service = %q", seen.Get("service"))
	}
	// The comma inside the quoted scope must survive the split, or the token
	// comes back for a scope nobody asked for.
	if seen.Get("scope") != "repository:softwarity/plug:pull,push" {
		t.Errorf("scope = %q", seen.Get("scope"))
	}
	if _, ok := seen["error"]; ok {
		t.Errorf("the challenge's own diagnostics were forwarded to the auth server: %v", seen)
	}
	if _, ok := seen["realm"]; ok {
		t.Errorf("realm must address the request, not ride in it: %v", seen)
	}
}
