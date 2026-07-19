#!/usr/bin/env bash
# Shared tail of the three cluster serve scripts (compose / k8s / swarm): idle
# while the CALLER run (the CI pipeline that dispatched this cluster) is still
# running, and shut down within a minute of it finishing — INCLUDING when it
# was cancelled, the case kill-cluster can never handle (a cancelled run's jobs
# are killed with it, and its orphaned clusters used to squat the 20-runner
# pool for their whole TTL, starving the next pipeline's serves past the legs'
# cluster wait). The TTL stays as the last-resort backstop: if gh can't answer
# (token, API blip) the loop just runs it out, the pre-fix behaviour.
#
#   PLUG_CLUSTER_IDENT=<caller-run-id>-<suffix>  (the corr id)
#   PLUG_CLUSTER_TTL=<seconds>                   (backstop)
#   GH_TOKEN                                     (for gh run view; actions:read)
set -u
ttl="${PLUG_CLUSTER_TTL:-1800}"
caller="${PLUG_CLUSTER_IDENT%-*}"
echo "=== cluster up — serving while caller run $caller lives (TTL backstop ${ttl}s) ==="
end=$(($(date +%s) + ttl))
while [ "$(date +%s)" -lt "$end" ]; do
  st="$(gh run view "$caller" --repo "${GITHUB_REPOSITORY:-softwarity/plug}" \
        --json status --jq .status 2>/dev/null || echo unknown)"
  if [ "$st" = "completed" ]; then
    echo "caller run $caller is done — shutting down"
    exit 0
  fi
  sleep 60
done
echo "TTL backstop reached"
