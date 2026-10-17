#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# doctor: the health checks must pass on a healthy leg. Read-only end to end
# (local state + this leg's profile against the real agent); non-interactive
# stdin, so the issue prompt never fires. Exit 0 = no ✗ finding on a machine
# the install one-liner just set up. Sourced by scripts/ci/e2e-matrix.sh after
# lib.sh.
#
# -p e2e-a, the profile the installer saved: the cluster-B cells run beside this
# one (e2e-background.sh) and the admission cell adds profiles of its own for a
# while, which a doctor reading every profile would check too.
#
# A -s session runs beside doctor, so the sessions section is read on a live
# one: doctor must list it under e2e-a (started with --host, it is found by its
# agent's address) and find its name answering end to end, through the cluster
# to the local process. The session ends by its -stop-file, never by kill: a
# kill skips the teardown on Windows (see do_takeover).
do_doctor() {
  echo "=== doctor: the health checks must pass on a healthy leg ==="
  local dname="doctor-$leg" dlocal=18190
  if ! helper_bin echo-local; then
    echo "--- doctor FAIL - echo-local did not build"; sum "**doctor** ❌ (build)"; return 1
  fi
  local d_stop; d_stop="$(stop_file doctor)"
  "$PLUG" --host "$ip" --port "$port" -s "$dname:$dlocal:$dlocal" \
    "$root/echo-local$ext" -addr "127.0.0.1:$dlocal" -text "doctor-ok" -ttl 120s -stop-file "$d_stop" >/tmp/doctor-serve.out 2>&1 &
  local d_pid=$! i=0
  until grep -q "path verified" /tmp/doctor-serve.out 2>/dev/null || [ $i -ge 30 ]; do sleep 1; i=$((i + 1)); done

  local dr rc
  dr="$(plug_bounded 60 doctor -p e2e-a </dev/null 2>&1)"; rc=$?
  : > "$d_stop"
  wait_bg "$d_pid" "the -s session beside doctor (ends on its -stop-file, or its -ttl)" 140

  if [ $rc -ne 0 ] || ! printf '%s' "$dr" | grep -q "agent"; then
    echo "--- doctor FAIL -"; printf '%s\n' "$dr" | tail -20 | sed 's/^/    /'
    sum "**doctor** ❌"; return 1
  fi
  # By its words, not its glyph: a Windows console may not carry the check mark.
  if ! printf '%s' "$dr" | grep "served $dname " | grep -q "$dname:$dlocal -> localhost:$dlocal answers"; then
    echo "--- doctor FAIL - the live session serving $dname is not listed as answering; got:"
    printf '%s\n' "$dr" | grep -A1 "served" | sed 's/^/    /'
    echo "    --- session output ---"; tail -8 /tmp/doctor-serve.out 2>/dev/null | sed 's/^/    /'
    sum "**doctor** ❌ - the live session is not listed as answering"; return 1
  fi
  echo "doctor OK - all checks green on this leg, the live session listed with its name answering"
  sum "**doctor (sessions listed, each name probed end to end)** ✅"; return 0
}
