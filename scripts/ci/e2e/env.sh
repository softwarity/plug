#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# env passthrough: the child must see the caller's environment (a user's
# `FOO=bar plug npm start` / dotenv workflow depends on env AND cwd surviving
# plug's launcher → core → shim chain untouched).
#
# This cell used to carry six more assertions under its one name (workload
# env, privilege drop, DNS honesty, DNS relay, client-only, doctor), so with
# the fail-fast the first red masked the other six and the step's name said
# nothing about which had failed. Each is its own cell now, in that order,
# right after this one. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
do_env() {
  echo "=== env passthrough ==="
  local ev
  ev="$(PLUG_E2E_CANARY=canary-42 plug_bounded 45 --host "$ip" --port "$port" $serve \
    bash -c 'echo "$PLUG_E2E_CANARY"' 2>/dev/null | tr -d '\r' | tail -1)"
  if [ "$ev" = "canary-42" ]; then
    echo "env OK - the child sees the caller's variables"; sum "**env passthrough** ✅"
  else
    echo "--- env FAIL - child saw '${ev:-<nothing>}' (want canary-42)"; sum "**env passthrough** ❌"; return 1
  fi
}
