package agent

import (
	"strings"
	"testing"
)

// One answer-reading path for both orchestrators, because there were two and
// they had stopped agreeing: the Docker one returned the decode error, the
// Kubernetes one dropped it on the floor with `_ =`. An answer the agent could
// not read came back from Kubernetes as "200, everything fine" with the output
// left at its zero value, and the caller acted on that. A Service read as having
// no selector and no ports looks exactly like a Service that really has none,
// and the repoint that follows is made on nothing.
func TestAnUnreadableSuccessIsNotASuccess(t *testing.T) {
	var out struct {
		Spec struct {
			Selector map[string]string `json:"selector"`
		} `json:"spec"`
	}
	status, err := readAPIReply(200, "200 OK", []byte("<html>a proxy sat in the way</html>"), &out, false)
	if err == nil {
		t.Fatal("an answer that is not even JSON was reported as a success, and the caller now believes " +
			"the Service it asked about has no selector")
	}
	if status != 200 {
		t.Errorf("status %d, want the status the API really sent", status)
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("the error does not say what went wrong: %v", err)
	}
}

// A refusal must carry the reason the API gave, not just its status line: that
// message is the only thing that tells a permission problem from a missing
// object.
func TestARefusalKeepsTheReasonTheAPIGave(t *testing.T) {
	body := []byte(`{"message":"services \"api\" is forbidden: RBAC denies patch"}`)
	for _, withBody := range []bool{true, false} {
		_, err := readAPIReply(403, "403 Forbidden", body, nil, withBody)
		if err == nil {
			t.Fatal("a 403 came back as success")
		}
		if !strings.Contains(err.Error(), "RBAC denies patch") {
			t.Errorf("withBody=%v lost the API's own message, leaving only a status code to act on: %v",
				withBody, err)
		}
	}
}

// Docker often refuses with a bare string and no JSON around it, so quoting the
// raw body is the only way to say anything useful. Kubernetes always sends a
// Status object, whose full text is a wall.
func TestABareTextRefusalIsQuotedOnlyWhereItHelps(t *testing.T) {
	body := []byte("no such container: signpost-api")
	if _, err := readAPIReply(404, "404 Not Found", body, nil, true); !strings.Contains(err.Error(), "no such container") {
		t.Errorf("Docker's own words were dropped, leaving 404 Not Found and nothing to act on: %v", err)
	}
	_, err := readAPIReply(404, "404 Not Found", body, nil, false)
	if strings.Contains(err.Error(), "no such container") {
		t.Errorf("the Kubernetes path quoted a raw body it never sends: %v", err)
	}
}

// And a success with nothing to decode stays a success. The callers that pass no
// output are the ones doing a patch or a scale, which is most of them.
func TestASuccessWithNothingToDecodeIsStillASuccess(t *testing.T) {
	if _, err := readAPIReply(200, "200 OK", []byte("anything at all"), nil, false); err != nil {
		t.Errorf("a caller wanting no output was handed an error: %v", err)
	}
}
