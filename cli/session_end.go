package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// A run is one session on the agent: every connection it opens presents the
// same id (tunnel/session.go), so a gateway lists "alice's plug -s checkout"
// and can end it. Ending it is a notice, not a dropped line: the run then
// stops its command as a SIGTERM would, its teardown runs, and plug exits with
// exitSessionClosed after saying who ended it and why.
//
// The datapath a macOS or Windows machine shares between its runs presents no
// session (its config is built without one), so a run there holds a
// connection of its own for its whole life (holdSession): that is what the
// session is, and what the notice reaches.

// exitSessionClosed is plug's exit status when the cluster ended the session:
// EX_NOPERM, so a script can tell it from the command's own failure.
const exitSessionClosed = 77

// newRunSession is the identity of this run, the same on all its connections.
func newRunSession() tunnel.Session {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return tunnel.Session{}
	}
	return tunnel.Session{ID: hex.EncodeToString(b), Version: version}
}

var (
	sessionEndOnce sync.Once
	sessionEndErr  error
	sessionEndMu   sync.Mutex
)

// watchSessionEnd hears the agent ending this run's session on tr, and stops
// the command. Every connection of the run registers; the first notice acts.
func watchSessionEnd(cfg config, tr *tunnel.Transport) {
	tr.OnSessionClosed(func(err error) {
		sessionEndOnce.Do(func() {
			sessionEndMu.Lock()
			sessionEndErr = err
			sessionEndMu.Unlock()
			info("%s. Stopping your command; this session will not reconnect.", sessionEndLine(cfg, err))
			tun.RequestStop()
		})
	})
}

// sessionEnded is the agent's notice, nil while the session has not been ended.
func sessionEnded() error {
	sessionEndMu.Lock()
	defer sessionEndMu.Unlock()
	return sessionEndErr
}

// sessionEndLine says who ended the session and why, naming the cluster as the
// person knows it.
func sessionEndLine(cfg config, err error) string {
	var ce *tunnel.ClosedError
	if errors.As(err, &ce) {
		return clusterName(cfg) + " closed this session: " + ce.Reason
	}
	return clusterName(cfg) + " closed this session"
}

// finishRun is the run's exit status: the command's, unless the cluster ended
// the session, which is then said once more, last, where it cannot scroll away
// under the command's own output.
func finishRun(cfg config, code int) int {
	if err := sessionEnded(); err != nil {
		info("%s", sessionEndLine(cfg, err))
		return exitSessionClosed
	}
	return code
}

// holdSession opens the connection a run holds when the datapath it uses is
// shared (macOS, the Windows service) and so presents no session: without it, a
// pure client would be invisible to the gateway and could not be ended.
func holdSession(cfg config) (func(), error) {
	if cfg.session.ID == "" {
		return func() {}, nil
	}
	tr, err := dialTunnel(cfg)
	if err != nil {
		return nil, err
	}
	return func() { tr.Close() }, nil
}
