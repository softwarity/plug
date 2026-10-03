#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# orphan. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# orphan: the session dies WITHOUT ever saying so - kill -9 on the whole tree,
# which is a closed lid, a crashed terminal, a network gone at the moment of
# Ctrl-C. No unserve reaches the agent. The deployed workload must come back on
# its own, and the agent must NOT be restarted to get there.
#
# This is the production incident the periodic sweep was written for: Kubernetes
# Services left pointing at a dead session port, put right only by a
# `kubectl rollout restart` of the agent, because the sweep that restores parked
# workloads ran at boot and never again. `takeover` above ends its session
# cleanly and `resilience` kills the AGENT; nothing killed the session.
#
# The budget is the sweep: one pass a minute, a liveness probe, then the
# orchestrator putting the workload back. 150s covers the worst case of just
# having missed a pass. The local echo carries a -ttl because on Windows a
# TerminateProcess on plug.exe does not reach its native children, and a
# leftover listener must not outlive the cell.
hard_kill_tree() {
  for hk_c in $(ps_all | awk -v p="$1" '$2==p {print $1}'); do
    hard_kill_tree "$hk_c"
  done
  kill -9 "$1" 2>/dev/null
}
do_orphan() {
  local tname="$tko_name" tport="$tko_port"
  echo "=== orphan: take $tname over, kill the session with no unserve, wait for it to come back by itself ==="
  if ! helper_bin echo-local; then
    echo "--- orphan FAIL - echo-local did not build"; sum "**orphaned takeover (periodic sweep)** ❌ (build)"; return 1
  fi
  probe() { prober_fetch "http://$tname:$tport/"; }

  if ! retry_until 5 3 "deployed-$tname" probe; then
    echo "--- orphan FAIL - baseline: prober said '${ru_out:-nothing}' (want deployed-$tname)"
    sum "**orphaned takeover (periodic sweep)** ❌ - baseline"; return 1
  fi

  "$PLUG" --host "$ip" --port "$port" -s "$tname:$tport:18098" \
    "$root/echo-local$ext" -addr 127.0.0.1:18098 -text "orphan-$tname" -ttl 120s >/tmp/orphan.out 2>&1 &
  local or_pid=$! during=""
  sleep 8 # arm + park + end-to-end verify
  retry_until 3 3 "orphan-$tname" probe; during="$ru_out"
  if [ "$during" != "orphan-$tname" ]; then
    hard_kill_tree "$or_pid"; wait "$or_pid" 2>/dev/null || true
    echo "--- orphan FAIL - the takeover never answered: prober said '${during:-nothing}' (want orphan-$tname)"
    echo "    --- session output ---"; tail -12 /tmp/orphan.out 2>/dev/null | sed 's/^/    /'
    sum "**orphaned takeover (periodic sweep)** ❌ - takeover never answered"; return 1
  fi

  # The kill. Depth-first and -9: nothing in the tree gets to run a teardown.
  hard_kill_tree "$or_pid"; wait "$or_pid" 2>/dev/null || true
  local killed_at; killed_at=$(date +%s)

  local after="" waited=0
  while [ "$waited" -lt 150 ]; do
    after="$(probe)"
    [ "$after" = "deployed-$tname" ] && break
    sleep 5
    waited=$(( $(date +%s) - killed_at ))
  done
  waited=$(( $(date +%s) - killed_at ))

  # The first deployed answer is not the restore, it is the start of it. The
  # incident this cell covers is precisely a restore that is PARTIAL: an
  # EndpointSlice still pointing at the dead session, half the requests lost,
  # and a single read that happened to land on the right half said "deployed".
  # So: ten reads after the first, all deployed, or the dead session's route
  # is still in (the same rule as takeover's restore, through assert_all).
  if [ "$after" = "deployed-$tname" ]; then
    sleep 3
    aa_clock=$killed_at
    assert_all probe "deployed-$tname" 10 "deployed- orphan-" || after="$aa_why"
  fi

  if [ "$after" = "deployed-$tname" ]; then
    echo "orphan OK - session killed with no unserve, deployed $tname answering again after ${waited}s (10/10 reads), agent never restarted"
    sum "**orphaned takeover (periodic sweep)** ✅ (${waited}s)"; return 0
  fi
  echo "--- orphan FAIL - ${waited}s after the kill the prober says '${after:-nothing}' (want deployed-$tname, every time): either the workload is still parked (only an agent restart would bring it back), or the restore left the dead session's route in (a split read above)"
  echo "    --- session output ---"; tail -12 /tmp/orphan.out 2>/dev/null | sed 's/^/    /'
  sum "**orphaned takeover (periodic sweep)** ❌ - still parked after ${waited}s"; return 1
}
