#!/usr/bin/env bash
# Print the release BEFORE the newest published one — the version a developer
# who is one update behind is running.
#
# The e2e needs an agent on some earlier release to prove `plug update` rolls a
# deployment for real. That fixture used to be pinned at 2.4.1, which aged into
# two problems: it re-tested bugs already fixed several releases ago (a failure
# there says nothing about the code under test), and it would take all three
# cluster families down the day that tag left the registry. Resolving it means
# the fixture is always the realistic upgrade path, and never rots.
set -uo pipefail

repo="${1:-softwarity/plug}"
page="https://hub.docker.com/v2/repositories/${repo}/tags?page_size=100"

tags="$(curl -fsS --max-time 20 "$page" 2>/dev/null)" || {
  echo "cannot list the tags of $repo — is the registry reachable?" >&2
  exit 1
}

# x.y.z only: x.y and x are moving aliases, and a stream tag (latest, main, a
# branch) is not a release at all.
prev="$(printf '%s' "$tags" \
  | tr ',' '\n' | sed -n 's/.*"name": *"\([0-9][0-9.]*\)".*/\1/p' \
  | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' \
  | sort -t. -k1,1n -k2,2n -k3,3n -u \
  | tail -2 | head -1)"

if [ -z "$prev" ]; then
  echo "no x.y.z release found among the tags of $repo" >&2
  exit 1
fi
echo "$prev"
