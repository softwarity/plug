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
#   PLUG_CALLER_RUN_ID=<run-id>                        (authoritative, if the caller passes it)
#   PLUG_CLUSTER_IDENT=<run-id>-<attempt>-<suffix>     (the corr id; the fallback source)
#   PLUG_CLUSTER_TTL=<seconds>                         (backstop)
#   GH_TOKEN                                           (for gh run view; actions:read)
set -u
ttl="${PLUG_CLUSTER_TTL:-1800}"
# The run id is the FIRST field of the corr id. Reading it as "everything but
# the last field" was right only while the corr id was <run-id>-<suffix>; once
# the re-run fix added the attempt it yielded <run-id>-<attempt>, gh 404'd on
# that forever, and `|| echo unknown` swallowed every 404. Result: no cluster
# ever left early again. All six sat out their full TTL (1200s compose, 2100s
# k8s/swarm) on a 20-runner pool, which is the famine this script exists to
# prevent. Prefer an id the caller states outright; only then fall back to
# splitting, and say so out loud when the split does not yield a run id.
caller="${PLUG_CALLER_RUN_ID:-${PLUG_CLUSTER_IDENT%%-*}}"
case "$caller" in
  '' | *[!0-9]*)
    echo "::warning::cluster '${PLUG_CLUSTER_IDENT:-}' has no caller run id (got '$caller'): it cannot see its caller finish and will hold a runner for the whole ${ttl}s TTL"
    caller=""
    ;;
esac
echo "=== cluster up: serving while caller run ${caller:-<none>} lives (TTL backstop ${ttl}s) ==="
end=$(($(date +%s) + ttl))
warned=0
while [ "$(date +%s)" -lt "$end" ]; do
  if [ -n "$caller" ]; then
    st="$(gh run view "$caller" --repo "${GITHUB_REPOSITORY:-softwarity/plug}" \
          --json status --jq .status 2>/dev/null || echo unknown)"
    if [ "$st" = "completed" ]; then
      echo "caller run $caller is done, shutting down"
      exit 0
    fi
    # The TTL is still the backstop when gh cannot answer, but a mute backstop
    # is how a broken early exit went unnoticed for a whole release cycle.
    if [ "$st" = "unknown" ] && [ "$warned" = 0 ]; then
      warned=1
      echo "::warning::gh cannot read caller run $caller (token, or the API): falling back to the ${ttl}s TTL, so this cluster holds a runner until then"
    fi
  fi
  sleep 60
done
echo "TTL backstop reached"
