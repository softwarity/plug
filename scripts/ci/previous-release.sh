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
# Overridable so the resolution can be exercised against a fixture (and against a
# registry mirror) without talking to Docker Hub.
api="${PLUG_HUB_API:-https://hub.docker.com/v2/repositories}"

# EVERY page, not just the first one. The tag list comes back ordered by
# last_updated descending, and each push to main adds three sha-* tags at the
# head; the repo passed 189 tags while page_size caps at 100. So reading page 1
# alone was a window that slid off the releases: after a couple of dozen pushes
# only the CURRENT release still fitted on it, `tail -2 | head -1` on a one-line
# list returned that current release as the "previous" one, and updatejump /
# updatenotify failed on all nine legs blaming the code for refusing to update an
# agent that was already up to date. A push or two later, zero releases fitted,
# this script exited 1, and the six clusters never started: nine legs reporting
# "cluster never became reachable" for a paging bug. Draining the list costs two
# requests today and cannot slide.
releases=""
page=1
while [ "$page" -le 25 ]; do
  body="$(curl -fsS --max-time 20 "${api}/${repo}/tags?page_size=100&page=${page}" 2>/dev/null)" || {
    echo "cannot list the tags of $repo (page $page): is the registry reachable?" >&2
    exit 1
  }
  # x.y.z only: x.y and x are moving aliases, and a stream tag (latest, main, a
  # branch) is not a release at all.
  found="$(printf '%s' "$body" \
    | tr ',' '\n' | sed -n 's/.*"name": *"\([0-9][0-9.]*\)".*/\1/p' \
    | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$')"
  [ -n "$found" ] && releases="$releases$found
"
  # Stop on the API's own end-of-list marker. Asking for one page too many is a
  # 404, which curl -f turns into the "registry unreachable" exit above: a paging
  # detail would read as an outage and take the six clusters down with it.
  # Matched with sed, not `grep -q`, whose early exit makes `tr` die of SIGPIPE
  # and the pipeline return 141 under `pipefail` even on a match.
  next="$(printf '%s' "$body" | tr ',' '\n' | sed -n 's/.*"next" *: *//p' | head -1)"
  case "$next" in null*) break ;; esac
  page=$((page + 1))
done

sorted="$(printf '%s' "$releases" | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' \
  | sort -t. -k1,1n -k2,2n -k3,3n -u)"
count="$(printf '%s\n' "$sorted" | grep -c . || true)"

# The invariant, said out loud. "The release before the newest one" needs two
# releases to exist; with one, tail -2 | head -1 quietly hands back the newest and
# the update cells go hunting for a bug in plug that is not there.
if [ "$count" -lt 2 ]; then
  echo "need two x.y.z releases in $repo to name the previous one, found $count${sorted:+ ($(printf '%s' "$sorted" | tr '\n' ' '))}" >&2
  echo "the e2e update cells upgrade an agent FROM the previous release TO the newest; there is no such pair here" >&2
  exit 1
fi

printf '%s\n' "$sorted" | tail -2 | head -1
