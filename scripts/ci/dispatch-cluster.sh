#!/usr/bin/env bash
# Dispatch the cluster workflow and echo (on stdout, nothing else) the run id of
# the run it created, so the caller can `gh run cancel` it at the end. Used by
# .github/workflows/e2e-mesh.yml (the `start-cluster` job).
#
# `gh workflow run` does not return the created run id, so we tag the cluster run
# with a deterministic name (`run-name: cluster-for-<corr>` in cluster.yml) and
# poll `gh run list` for it. Needs GH_TOKEN in the env.
set -euo pipefail
corr="${1:?usage: dispatch-cluster.sh <corr-id>}"

# Trigger it ON THIS RUN'S REF (a branch run must get a cluster built from its
# own branch); keep gh's chatter off stdout (stdout must carry only the id).
gh workflow run cluster.yml -r "${GITHUB_REF_NAME:-main}" -f corr="$corr" >&2

echo "waiting for the cluster run (cluster-for-$corr) to appear..." >&2
for _ in $(seq 1 20); do
  id="$(gh run list --workflow=cluster.yml --json databaseId,displayTitle \
        --jq ".[] | select(.displayTitle==\"cluster-for-$corr\") | .databaseId" | head -1)"
  if [ -n "$id" ]; then
    echo "found cluster run $id" >&2
    echo "$id"
    exit 0
  fi
  sleep 4
done

echo "cluster run for corr=$corr never appeared" >&2
exit 1
