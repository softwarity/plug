#!/usr/bin/env bash
# Cancel the cluster runs a family dispatched, and SAY what happened to each.
# Used by the three kill-<family> jobs of ci.yml, which differ only in which
# legs they wait for (that is `needs`, and it cannot be shared).
#
#   kill-clusters.sh <run-id>:<label> [<run-id>:<label>...]    (GH_TOKEN set)
#
# No checkout on those jobs, so --repo is passed explicitly, otherwise the
# cancel silently fails and the cluster runs sleep out their full TTL.
#
# `|| true` used to hide exactly the failure the --repo fix was about. A 403
# from a narrowed token, a 502, an id the dispatch never recorded: all four
# outcomes looked the same from outside, a green step, while the cluster kept a
# runner out of a 20-runner pool for the rest of its TTL. Only "it had already
# finished" is normal here; the rest gets annotated, and the job still passes
# so a stuck cancel never reds a good pipeline.
set -u
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must name the repository}"
for pair in "$@"; do
  id="${pair%%:*}"; who="${pair##*:}"
  if [ -z "$id" ]; then
    echo "::warning::no run id was recorded for the $who cluster: if it was dispatched, nothing cancels it and it holds a runner until its TTL expires"
  elif out="$(gh run cancel "$id" --repo "$repo" 2>&1)"; then
    echo "cancelled the $who cluster, run $id"
  else
    case "$out" in
      *"Cannot cancel a workflow run that is completed"*)
        echo "the $who cluster (run $id) had already finished" ;;
      *)
        echo "::warning::could not cancel the $who cluster, run $id: $out. It holds a runner until its TTL expires." ;;
    esac
  fi
done
