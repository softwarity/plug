#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# collision. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# collision: a name ANOTHER live plug session already serves must be REFUSED -
# the guard the takeover default deliberately keeps (takeover parks DEPLOYED
# workloads only, never another dev's session). A deployed name is no longer
# refused (it is parked - do_takeover proves that), so the cell holds a session
# of its own open and asserts a second one on the same name bounces. Name and
# cluster port are per-leg (the legs run concurrently on the shared cluster).
do_collision() {
  local cname="col-$leg" cport="$col_port"
  echo "=== collision: a second -s on $cname (held by a live session) must be refused ==="
  if ! helper_bin echo-local; then
    echo "--- collision FAIL - echo-local did not build"; sum "**collision refused** ❌ (build)"; return 1
  fi
  # Session A holds the name for ~35s (natural end via -ttl - see do_takeover
  # for why kill is not an option on Windows).
  "$PLUG" --host "$ip" --port "$port" -s "$cname:$cport:18098" \
    "$root/echo-local$ext" -addr 127.0.0.1:18098 -text "col-a" -ttl 24s >/tmp/collision-a.out 2>&1 &
  local a_pid=$!
  sleep 8 # arm + verify
  # Session B, same name, while A lives: must bounce (the agent-side port is
  # held by A's remote-forward; the signpost also answers to A).
  local co
  # perl's alarm, like every other cell: a plug that hangs here must fail the
  # cell in seconds, not sit until the job's 25-minute timeout. It did exactly
  # that once - a prompt on a Windows runner with no console to answer it.
  co="$(perl -e 'alarm 60; exec @ARGV or exit 127' \
        "$PLUG" --host "$ip" --port "$port" -s "$cname:$cport:9" curl --version 2>&1 || true)"
  wait_bg "$a_pid" "the collision session A" 60
  if printf '%s' "$co" | grep -qiE "another session|denied by peer|already"; then
    # The refusal must NAME the holder's agent port. That port is what lets a
    # session tell ITS OWN forgotten holder from a stranger's, and so what
    # decides whether it may offer to stop it - without it the offer would be
    # made on a record alone, i.e. possibly on a PID the OS has since reused.
    if ! printf '%s' "$co" | grep -q "agent port [0-9]"; then
      echo "--- collision FAIL - refused, but the refusal names no agent port; got:"
      printf '%s\n' "$co" | tail -5 | sed 's/^/    /'
      sum "**collision refused** ❌ - no agent port named"; return 1
    fi
    # And with NO TERMINAL - which is every CI job, every script - it must
    # refuse outright: never prompt (nothing could answer, so the session would
    # hang forever) and never stop the holder unasked.
    if printf '%s' "$co" | grep -qi "stop it and take the name"; then
      echo "--- collision FAIL - prompted for confirmation with no terminal to answer on"
      sum "**collision refused** ❌ - prompted without a tty"; return 1
    fi
    echo "collision OK - the second session on $cname was refused (port named, no prompt without a tty)"
    sum "**collision refused (names the holder's port, never prompts headless)** ✅"; return 0
  fi
  echo "--- collision FAIL - the second -s on $cname was not refused; got:"
  printf '%s\n' "$co" | tail -5 | sed 's/^/    /'
  echo "    --- session A output ---"; tail -6 /tmp/collision-a.out 2>/dev/null | sed 's/^/    /'
  sum "**collision refused** ❌"; return 1
}
