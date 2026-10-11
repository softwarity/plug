#!/usr/bin/env bash
# symptoms.sh [runs] - every symptom the e2e marked (E2E-SYMPTOM, see symptom()
# in scripts/ci/e2e/lib.sh) in the last <runs> CI runs on main, 30 by default.
# One line per sighting: day, commit, run, then what was marked. A red cell seen
# once is an anecdote; this is how it stops being one.
#
# Only red and cancelled runs are read: a symptom is checked when a cell fails,
# and GitHub keeps run logs for 90 days, so that is as far back as it can see.
set -uo pipefail
n="${1:-30}"
repo=softwarity/plug
gh run list -R "$repo" --workflow ci.yml --branch main -L "$n" \
  --json databaseId,conclusion,createdAt,headSha \
  -q '.[] | select(.conclusion=="failure" or .conclusion=="cancelled") | "\(.databaseId) \(.createdAt[0:10]) \(.headSha[0:7])"' |
  while read -r id day sha; do
    gh run view "$id" -R "$repo" --log-failed 2>/dev/null | grep -o 'E2E-SYMPTOM .*' | sort -u |
      sed "s|^E2E-SYMPTOM |$day $sha https://github.com/$repo/actions/runs/$id  |"
  done
