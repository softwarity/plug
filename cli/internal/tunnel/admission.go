package tunnel

// plug's side of an agent that admits its members by approval (the agent's
// admission.go). Such an agent lets a key it does not know sign, then opens a
// keyboard-interactive exchange named admissionChallenge: one question, which
// machine this is, then one message, what became of the key. That exchange is
// recognised by its name and answered here; any other prompt is a human's and
// gets no answer, since plug has nobody to ask.
//
// The words the developer reads are the CLI's, built from what the agent said
// (AuthFailure.Pending, Transport.Welcomed): the CLI knows the profile, which a
// command to type has to name, and the agent does not.

import (
	"errors"
	"os"
	"strings"
	"sync"
)

// Must match the agent's admission.go, word for word: these are the protocol.
const (
	admissionChallenge = "plug-admission"
	admissionAskName   = "ask-name"
	admissionWelcome   = "welcome"
	admissionOwnKey    = "own-key"
	admissionPending   = "pending"
)

// MachineName is how this machine introduces itself to an agent that admits by
// approval, shown to the member who accepts it: the account and the host,
// "alice@laptop". The agent reduces it to what a list can show. A var so the
// tests can set it.
var MachineName = func() string {
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME") // Windows
	}
	host, _ := os.Hostname()
	host, _, _ = strings.Cut(host, ".") // the short name: laptop, not laptop.corp.example
	switch {
	case user != "" && host != "":
		return user + "@" + host
	case host != "":
		return host
	}
	return user
}

// admissionExchange answers the agent's exchange during one dial and keeps
// the last thing it said.
type admissionExchange struct {
	mu   sync.Mutex
	last string
}

func (a *admissionExchange) answer(name, instruction string, questions []string, _ []bool) ([]string, error) {
	if name != admissionChallenge {
		return nil, errors.New("the agent asked for a password, and plug authenticates with keys only")
	}
	if instruction == admissionAskName && len(questions) == 1 {
		return []string{MachineName()}, nil
	}
	a.mu.Lock()
	a.last = instruction
	a.mu.Unlock()
	return make([]string, len(questions)), nil
}

func (a *admissionExchange) said() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}
