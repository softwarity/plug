#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# outage. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# outage recovery: a service DOWN then COMING BACK must become reachable within
# the SAME live session. Each OS leg has its own flaky instance (shared cluster).
do_outage() {
  local flaky="$flaky_name"
  echo "=== outage recovery: $flaky down → up inside one session ==="
  local ol
  ol="$(plug_to "$ip" bash -c '
    f="$0"
    curl -s --max-time 6 "http://$f:8099/" >/dev/null 2>&1 && { echo "flaky answered while it should be down"; exit 1; }
    curl -s --max-time 10 "http://$f:8098/up" >/dev/null || { echo "control endpoint unreachable"; exit 1; }
    sleep 2
    for _ in 1 2 3 4 5; do
      out=$(curl -s --max-time 6 "http://$f:8099/" 2>/dev/null) && [ "$out" = "flaky-ok" ] && { echo recovered; exit 0; }
      sleep 2
    done
    echo "never recovered"; exit 1
  ' "$flaky" 2>/tmp/outage.err | tr -d '\r' | tail -1)"
  if [ "$ol" = "recovered" ]; then
    echo "outage OK - the service came back and the same session reached it"; sum "**outage recovery** ✅"; return 0
  fi
  echo "--- outage FAIL - $ol"; tail -8 /tmp/outage.err 2>/dev/null | sed 's/^/    /'
  sum "**outage recovery** ❌ - $ol"; return 1
}
