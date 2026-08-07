#!/usr/bin/env bash
# Dispatch a cluster workflow (compose-for.yml by default, k8s-for.yml for the
# kubernetes family) and echo (on stdout, nothing else) the run id of the run it
# created, so the caller can `gh run cancel` it at the end. Used by ci.yml's
# `start-cluster` job.
#
# `gh workflow run` does not return the created run id, so we tag the cluster
# run with a deterministic name (run-name: <workflow>-<corr>) and poll
# `gh run list` for it. Needs GH_TOKEN in the env.
set -euo pipefail
corr="${1:?usage: dispatch-cluster.sh <corr-id> [workflow.yml]}"
wf="${2:-compose-for.yml}"
title="${wf%.yml}-$corr" # each workflow names its runs `run-name: <basename>-<corr>`

# Trigger it ON THIS RUN'S REF (a branch run must get a cluster built from its
# own branch); keep gh's chatter off stdout (stdout must carry only the id).
# The image ci.yml just published for this commit, so the cluster PULLS the
# artefact we ship instead of building its own. Empty is allowed (a manual
# dispatch): the cluster then builds from its checkout, as it always did.
gh workflow run "$wf" -r "${GITHUB_REF_NAME:-main}" -f corr="$corr" -f image="${CLUSTER_IMAGE:-}" >&2

echo "waiting for the cluster run ($title) to appear..." >&2
for _ in $(seq 1 20); do
  id="$(gh run list --workflow="$wf" --json databaseId,displayTitle \
        --jq ".[] | select(.displayTitle==\"$title\") | .databaseId" | head -1)"
  if [ -n "$id" ]; then
    echo "found cluster run $id" >&2
    echo "$id"
    exit 0
  fi
  sleep 4
done

echo "cluster run for corr=$corr never appeared" >&2
exit 1
