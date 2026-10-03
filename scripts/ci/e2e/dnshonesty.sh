#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# dns honesty: a name ABSENT from the cluster must answer NXDOMAIN (plug asks
# the agent before minting), not hand out a fake IP that can only refuse the
# connect. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
do_dnshonesty() {
  echo "=== dns honesty: an absent name must NXDOMAIN ==="
  local nx
  nx="$(plug curl -sS --max-time 8 "http://absent-name-e2e:9/" 2>&1 | tr -d '\r' | tail -1)"
  if printf '%s' "$nx" | grep -qiE "could not resolve|no such host|name or service not known"; then
    echo "dns OK - absent-name-e2e answered NXDOMAIN (honest resolution failure)"
    sum "**dns honesty (absent → NXDOMAIN)** ✅"
  else
    echo "--- dns FAIL - expected a resolution error, got: ${nx:-<nothing>}"
    sum "**dns honesty (absent → NXDOMAIN)** ❌ - \`${nx:-nothing}\`"; return 1
  fi
}
