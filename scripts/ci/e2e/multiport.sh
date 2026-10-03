#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# multiport. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# --- one name, several cluster ports: the mail-gateway shape -------------------
#
# One process, one -s name, three cluster ports (HTTP+SMTP+POP3 style): one
# signpost carries the name and listens on ALL of them, each relayed to its own
# agent-allocated port. Every port must reach ITS OWN local listener from
# inside the cluster - reaching the wrong one is the bug this cell pins down.
do_multiport() {
  local name="multip-$leg" p1="$mp_p1" p2="$mp_p2" p3="$mp_p3"
  echo "=== multi-port: $name:18131+18132+18133, one process, each port its own backend ==="
  if ! helper_bin echo-local; then
    echo "--- multi-port FAIL - echo-local did not build"; sum "**one name, three ports** ❌ (build)"; return 1
  fi
  "$PLUG" --host "$ip" --port "$port" \
    -s "$name:18131:$p1" -s "$name:18132:$p2" -s "$name:18133:$p3" \
    "$root/echo-local$ext" -addr "127.0.0.1:$p1,127.0.0.1:$p2,127.0.0.1:$p3" \
    -text "mp-a-$name,mp-b-$name,mp-c-$name" >/tmp/multiport.out 2>&1 &
  local mp_pid=$!
  sleep 8 # arm + verify
  mp_a() { prober_fetch "http://$name:18131/"; }
  mp_b() { prober_fetch "http://$name:18132/"; }
  mp_c() { prober_fetch "http://$name:18133/"; }
  local ra="" rb="" rc=""
  for _ in 1 2 3; do
    ra="$(mp_a)"; rb="$(mp_b)"; rc="$(mp_c)"
    [ "$ra" = "mp-a-$name" ] && [ "$rb" = "mp-b-$name" ] && [ "$rc" = "mp-c-$name" ] && break
    sleep 3
  done
  # One right answer per port is not "each port its own backend": a relay that
  # picked a backend at random would pass one try in eight, and three tries
  # made that better than even. Once all three answer, three more reads each,
  # every one its own (assert_all keeps the sequence for the failure line).
  if [ "$ra" = "mp-a-$name" ] && [ "$rb" = "mp-b-$name" ] && [ "$rc" = "mp-c-$name" ]; then
    assert_all mp_a "mp-a-$name" 3 "mp-" || ra="$aa_why"
    assert_all mp_b "mp-b-$name" 3 "mp-" || rb="$aa_why"
    assert_all mp_c "mp-c-$name" 3 "mp-" || rc="$aa_why"
  fi
  stop_bg "$mp_pid" "the multi-port -s session"
  if [ "$ra" = "mp-a-$name" ] && [ "$rb" = "mp-b-$name" ] && [ "$rc" = "mp-c-$name" ]; then
    echo "multi-port OK - $name answers on 18131/18132/18133, each port its own backend"
    sum "**one name, three ports (no cross-talk)** ✅"; return 0
  fi
  echo "--- multi-port FAIL - got a='${ra:-nothing}' b='${rb:-nothing}' c='${rc:-nothing}'"
  tail -10 /tmp/multiport.out 2>/dev/null | sed 's/^/    /'
  sum "**one name, three ports** ❌"; return 1
}
