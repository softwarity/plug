#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# sameport. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# --- same cluster port, several names: the collision this build removed --------
#
# Inside the cluster every service has its own IP, so two services on :3000 are
# the NORMAL world (a NestJS fleet, say) - but every -s converges on the one
# agent, where a fixed port could bind only once: the second session bounced
# with "tcpip-forward request denied". The agent-side port is now allocated per
# session (the signpost relays <name>:<port> to it), so the cluster port stops
# being unique. Both names must answer THEIR OWN local service, from inside the
# cluster, at the same time.
do_sameport() {
  local na="samep-a-$leg" nb="samep-b-$leg" pa="$sp_pa" pb="$sp_pb"
  echo "=== same-port: $na:18120 AND $nb:18120, both live, both reachable ==="
  if ! helper_bin echo-local; then
    echo "--- same-port FAIL - echo-local did not build"; sum "**same cluster port ×2** ❌ (build)"; return 1
  fi
  "$PLUG" --host "$ip" --port "$port" -s "$na:18120:$pa"     "$root/echo-local$ext" -addr 127.0.0.1:$pa -text "same-$na" >/tmp/samep-a.out 2>&1 &
  local a_pid=$!
  "$PLUG" --host "$ip" --port "$port" -s "$nb:18120:$pb"     "$root/echo-local$ext" -addr 127.0.0.1:$pb -text "same-$nb" >/tmp/samep-b.out 2>&1 &
  local b_pid=$!
  sleep 8 # arm + verify, both
  sp_a() { prober_fetch "http://$na:18120/"; }
  sp_b() { prober_fetch "http://$nb:18120/"; }
  local ra="" rb=""
  for _ in 1 2 3; do
    ra="$(sp_a)"; rb="$(sp_b)"
    [ "$ra" = "same-$na" ] && [ "$rb" = "same-$nb" ] && break
    sleep 3
  done
  # The cross-talk this cell exists to refuse is two names on one port whose
  # relays answer each other's backend SOME of the time, and one matching read
  # per name lets that through one try in four. Once both answer, three more
  # reads each, every one its own (assert_all keeps the sequence for the
  # failure line).
  if [ "$ra" = "same-$na" ] && [ "$rb" = "same-$nb" ]; then
    assert_all sp_a "same-$na" 3 "same-" || ra="$aa_why"
    assert_all sp_b "same-$nb" 3 "same-" || rb="$aa_why"
  fi
  stop_bg "$a_pid" "the first same-port session"
  stop_bg "$b_pid" "the second same-port session"
  if [ "$ra" = "same-$na" ] && [ "$rb" = "same-$nb" ]; then
    echo "same-port OK - $na and $nb share :18120, each answered its own backend"
    sum "**same cluster port ×2 (no collision, no cross-talk)** ✅"; return 0
  fi
  echo "--- same-port FAIL - $na said '${ra:-nothing}', $nb said '${rb:-nothing}'"
  tail -8 /tmp/samep-a.out /tmp/samep-b.out 2>/dev/null | sed 's/^/    /'
  sum "**same cluster port ×2** ❌ - a='\`${ra:-nothing}\`' b='\`${rb:-nothing}\`'"; return 1
}
