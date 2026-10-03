#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# lease. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# lease: a live session keeps its name even when its SIGNPOST is gone. That
# state is not exotic - it is what a rebooted agent's boot-gc leaves behind, and
# what any failed re-provision leaves behind. Ownership used to be read off the
# signpost, so "no signpost" read as "name is free": a second session took a
# name its owner was still serving, and from then on the two overwrote each
# other's signpost on every reconnect, each leaving the other silently
# unreachable while everything LOOKED healthy. do_collision cannot catch this -
# there, A's signpost is present when B asks, which is the easy half.
#
# On every family: the sweep goes through the chaos service, which all three
# clusters deploy (compose.cluster.yml, swarm.cluster.yml, k8s.res-agents.yaml).
# The lease itself is taken in serveName, ABOVE the k8s/docker split, so it is
# the same code whichever backend the signpost lives in.
do_lease() {
  local lname="lease-$leg" lport="$lease_port" lloc="$lease_local"
  echo "=== lease: $lname stays its own session's after the signpost is swept ==="
  if ! helper_bin echo-local; then
    echo "--- lease FAIL - echo-local did not build"; sum "**name survives a swept signpost** ❌ (build)"; return 1
  fi
  # A holds the name for ~45s (natural end via -ttl - see do_takeover for why
  # kill is not an option on Windows).
  "$PLUG" --host "$ip" --port "$port" -s "$lname:$lport:$lloc" \
    "$root/echo-local$ext" -addr 127.0.0.1:$lloc -text "lease-a" -ttl 34s >/tmp/lease-a.out 2>&1 &
  local a_pid=$!
  sleep 8 # arm + verify

  # Baseline: A really is serving, so a later refusal means something.
  lease_probe() { prober_fetch "http://$lname:$lport/"; }
  if ! retry_until 3 3 "lease-a" lease_probe; then
    echo "--- lease FAIL - baseline: prober said '${ru_out:-nothing}' (want lease-a)"
    tail -8 /tmp/lease-a.out 2>/dev/null | sed 's/^/    /'
    wait_bg "$a_pid" "the lease session" 70; sum "**name survives a swept signpost** ❌ baseline"; return 1
  fi

  # Sweep A's signpost behind its back - exactly what a boot-gc does.
  local swept
  swept="$(plug curl -s --max-time 15 "http://chaos:8095/rm-signpost?name=$lname" 2>/dev/null | tr -d '\r' | tail -1)"
  if [ "$swept" != "removed" ]; then
    echo "--- lease FAIL - chaos could not sweep $lname's signpost (said '${swept:-nothing}')"
    wait_bg "$a_pid" "the lease session" 70; sum "**name survives a swept signpost** ❌ sweep"; return 1
  fi

  # B, same name, while A is still very much alive: must bounce. Bounded like
  # collision's second session: a refusal that turned into a prompt, or a
  # session that hung on the held name, used to sit until the cell watchdog.
  local co
  co="$(perl -e 'alarm 60; exec @ARGV or exit 127' \
        "$PLUG" --host "$ip" --port "$port" -s "$lname:$lport:9" curl --version 2>&1 || true)"
  wait_bg "$a_pid" "the lease session" 70
  if printf '%s' "$co" | grep -qiE "another live session|another session|already"; then
    echo "lease OK - $lname stayed its session's with no signpost to prove it"
    sum "**name survives a swept signpost** ✅"; return 0
  fi
  echo "--- lease FAIL - B took $lname while A held it (no signpost to read); got:"
  printf '%s\n' "$co" | tail -5 | sed 's/^/    /'
  echo "    --- session A output ---"; tail -6 /tmp/lease-a.out 2>/dev/null | sed 's/^/    /'
  sum "**name survives a swept signpost** ❌"; return 1
}
