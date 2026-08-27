#!/usr/bin/env bash
# Fetch each module's dependencies, tolerating a proxy that answers badly.
#
# Why this exists, precisely: proxy.golang.org answered ONE HTTP/2
# INTERNAL_ERROR while serving gvisor's .mod, and that failed the whole Windows
# unit-test leg of a green commit. `go` does not retry a bad response from the
# proxy, and every leg that compiles plug pulls gvisor, wireguard-go and
# x/crypto before it can do anything at all. So a bad minute upstream reads as a
# red build, on a day nobody changed anything.
#
# Three attempts with a growing pause. Not a loop until success: a proxy that is
# genuinely down should still fail the run, and quickly, rather than hold a
# runner for the length of the outage.
#
# Paired with the module cache on the same jobs (setup-go's `cache: true`),
# which is the other half: a warm cache does not reach the network at all, and
# this covers the first run after a go.sum change.
set -uo pipefail

for dir in "$@"; do
  ok=0
  for attempt in 1 2 3; do
    if (cd "$dir" && go mod download); then
      ok=1
      break
    fi
    echo "go mod download in $dir failed (attempt $attempt of 3), retrying"
    sleep $((attempt * 5))
  done
  if [ "$ok" != 1 ]; then
    echo "go mod download in $dir failed three times: the module proxy is not answering" >&2
    exit 1
  fi
done
