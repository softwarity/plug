package main

import (
	"errors"
	"path/filepath"
	"testing"
)

// The guards' value face: the same refusal fatal would have printed, as an
// error the daemon and the MCP server can carry, and recognisable as one so
// the daemon backs off instead of retrying it on every tick.
func TestTheKeyGuardsRefuseAsValues(t *testing.T) {
	cfg := config{key: filepath.Join(t.TempDir(), "absent.key")}
	keys, err := cfg.authKeysErr()
	if err == nil {
		t.Fatalf("an absent key was offered: %d keys", len(keys))
	}
	if !isLocalRefusal(err) {
		t.Errorf("the refusal is not recognised as local: %T", err)
	}
	// Wrapped, it is still one: applyDial sees whatever the dial chain added.
	if !isLocalRefusal(errors.Join(errors.New("dialling"), err)) {
		t.Error("a wrapped refusal is not recognised")
	}
	// And a profile with no key of its own offers the built-in one, as before.
	if keys, err := (config{}).authKeysErr(); err != nil || len(keys) != 1 {
		t.Errorf("the built-in key alone: %d keys, %v", len(keys), err)
	}
	// Plain errors are not refusals: an agent that is down must keep being
	// retried.
	if isLocalRefusal(errors.New("i/o timeout")) {
		t.Error("a network error was taken for a local refusal, and would be backed off")
	}
}
