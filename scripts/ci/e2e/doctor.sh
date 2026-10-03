#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# doctor: the health checks must pass on a healthy leg. Read-only end to end
# (local state + this leg's profile against the real agent); non-interactive
# stdin, so the issue prompt never fires. Exit 0 = no ✗ finding on a machine
# the install one-liner just set up. Sourced by scripts/ci/e2e-matrix.sh after
# lib.sh.
do_doctor() {
  echo "=== doctor: the health checks must pass on a healthy leg ==="
  local dr
  dr="$(plug_bounded 60 doctor </dev/null 2>&1)"
  if [ $? -eq 0 ] && printf '%s' "$dr" | grep -q "agent"; then
    echo "doctor OK - all checks green on this leg"
    sum "**doctor** ✅"; return 0
  fi
  echo "--- doctor FAIL -"; printf '%s\n' "$dr" | tail -20 | sed 's/^/    /'
  sum "**doctor** ❌"; return 1
}
