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
ident="${PLUG_CLUSTER_IDENT:-}" # unset must not trip `set -u`: the selftest runs without it
caller="${PLUG_CALLER_RUN_ID:-${ident%%-*}}"
case "$caller" in
  '' | *[!0-9]*)
    echo "::warning::cluster '${PLUG_CLUSTER_IDENT:-}' has no caller run id (got '$caller'): it cannot see its caller finish and will hold a runner for the whole ${ttl}s TTL"
    caller=""
    ;;
esac
# Which family this cluster serves, from the LAST field of the corr id: a|b are
# compose, ka|kb k8s, sa|sb swarm. It names the caller job whose completion
# means "every leg that needed this cluster is done": kill-<family>, which
# `needs` exactly those legs.
family_of() {
  case "${1##*-}" in
    a | b) echo compose ;;
    ka | kb) echo k8s ;;
    sa | sb) echo swarm ;;
    *) echo "" ;;
  esac
}
# The attempt is the middle field, <run-id>-<attempt>-<suffix>; a re-run has its
# own kill job and the first attempt's, long completed, must not be read for it.
attempt_of() {
  case "$1" in *-*-*) ;; *) echo 1; return ;; esac # no attempt field at all
  local rest="${1#*-}"
  case "${rest%%-*}" in '' | *[!0-9]*) echo 1 ;; *) echo "${rest%%-*}" ;; esac
}
if [ "${IDLE_SELFTEST:-}" = 1 ]; then
  fail=0
  check() { [ "$2" = "$3" ] || { echo "FAIL $1: got '$2', want '$3'"; fail=1; }; }
  check "compose a" "$(family_of 35598230832-1-a)" compose
  check "compose b" "$(family_of 35598230832-2-b)" compose
  check "k8s" "$(family_of 35598230832-1-kb)" k8s
  check "swarm" "$(family_of 35598230832-1-sa)" swarm
  check "unknown suffix" "$(family_of 35598230832-1-zz)" ""
  check "attempt 1" "$(attempt_of 35598230832-1-a)" 1
  check "attempt 3" "$(attempt_of 35598230832-3-kb)" 3
  check "no attempt field" "$(attempt_of 35598230832)" 1
  [ "$fail" = 0 ] && echo "idle selftest OK"
  exit "$fail"
fi
family="$(family_of "${PLUG_CLUSTER_IDENT:-}")"
attempt="$(attempt_of "${PLUG_CLUSTER_IDENT:-}")"
echo "=== cluster up: serving while caller run ${caller:-<none>} lives (family ${family:-<unknown>}, attempt $attempt, TTL backstop ${ttl}s) ==="
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
    # The caller being done is too late a signal when one family finishes well
    # before the others: compose legs end minutes before the k8s ones, and its
    # clusters held two runners until the whole pipeline was over. kill-<family>
    # is meant to end them with `gh run cancel`, and that cancel has been seen
    # accepted by the API and not acted on for six minutes, the serve step still
    # in progress. So do not depend on being told: once the caller's own
    # kill-<family> job has completed, every leg that needed this cluster is
    # over, whatever became of the cancel.
    if [ -n "$family" ]; then
      kill_st="$(gh api "repos/${GITHUB_REPOSITORY:-softwarity/plug}/actions/runs/$caller/attempts/$attempt/jobs?per_page=100" \
            --jq ".jobs[] | select(.name == \"kill $family clusters\") | .status" 2>/dev/null | head -1)"
      if [ "$kill_st" = "completed" ]; then
        echo "the caller's 'kill $family clusters' job is done: no leg needs this cluster any more, shutting down"
        exit 0
      fi
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
