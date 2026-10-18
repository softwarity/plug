package tun

import "sync"

// A stop asked of the command plug runs, by plug itself rather than by a
// signal: the agent ending the session (the tunnel package's ClosedError). The
// child is ended the way a SIGTERM aimed at plug ends it, so the session's
// teardown runs exactly as it does then. Asked once; asking again is nothing.

var (
	stopOnce sync.Once
	stopCh   = make(chan struct{})
)

// RequestStop asks the command plug is running to stop.
func RequestStop() { stopOnce.Do(func() { close(stopCh) }) }

// StopRequested is closed once RequestStop has been called.
func StopRequested() <-chan struct{} { return stopCh }
