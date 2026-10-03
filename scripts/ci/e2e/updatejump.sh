#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# updatejump. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# --- update: a RELEASE agent must retarget itself to the newest one -------------
#
# The starting point is the PREVIOUS published release, resolved when the
# cluster is built (scripts/ci/previous-release.sh) rather than pinned. That is
# the realistic upgrade path - what someone one release behind is running - and
# it never rots: a pinned old tag re-tests bugs fixed several releases ago, and
# takes every family down the day it leaves the registry.
#
# It must ask the registry, find a NEWER x.y.z than its own, and name it. Aiming
# at a version is what `plug update` is for - re-resolving the tag it already
# carries is what it used to do, and that returned the same image forever.
#
# Compose cannot recreate a container from inside it, so the verdict is "pulled"
# plus the command that finishes the job; the rollout itself is Swarm/k8s work,
# covered by the resilience cell. What this asserts is the DECISION.
do_updatejump() {
  local oldport="$prev_port"
  echo "=== update-jump (cluster B): an agent one release behind must retarget to the newest ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**update jump** ❌ (cluster B)"; return 1; }
  # Which release it starts from is the AGENT's answer, not something the
  # harness is told: the cluster deploys whatever previous-release.sh resolved
  # when it was built, and asking removes any chance of the two disagreeing.
  local prev
  prev="$(ssh_agent "$ip_b" "$oldport" version 2>/dev/null | tr -d '\r')"
  if ! printf '%s' "$prev" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "--- update-jump FAIL - the previous-release agent answered '${prev:-nothing}', not an x.y.z release"
    sum "**update jump** ❌ - no usable starting version"; return 1
  fi
  echo "    starting from the published $prev"

  # Bounded (180s: on compose the agent pulls the release before answering).
  local out rc=0
  out="$(perl -e 'alarm 180; exec @ARGV or exit 127' "$PLUG" --host "$ip_b" --port "$oldport" update </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  if [ "$rc" != 0 ]; then
    echo "--- update-jump FAIL - exit $rc"; sum "**update jump** ❌ - exit $rc"; return 1
  fi
  # The DECISION, asserted the same way on every backend: a release strictly
  # newer than the one it runs must be named.
  local target
  target="$(printf '%s' "$out" | sed -n 's|.*to [a-z0-9./-]*plug:\([0-9][0-9.]*\).*|\1|p' | head -1)"
  if [ -z "$target" ]; then
    echo "--- update-jump FAIL - the agent named no newer release (it should move off $prev)"
    sum "**update jump** ❌ - no target named"; return 1
  fi
  if [ "$target" = "$prev" ]; then
    echo "--- update-jump FAIL - retargeted to itself ($target)"
    sum "**update jump** ❌ - retargeted to itself"; return 1
  fi

  # Then what each backend can actually DO with that decision. Swarm rewrites
  # the service image and k8s patches the Deployment, so the agent really lands
  # on the new version and the CLI reports the move; Compose cannot recreate a
  # container from inside it, so it stops at "pulled" plus the command that
  # finishes the job. Assert whichever this cluster is, from the CLI's own line
  # - never skip: a backend that silently did nothing would look identical.
  agent_version() { ssh_agent "$ip_b" "$oldport" version 2>/dev/null | tr -d '\r'; }
  local now=""
  if printf '%s' "$out" | grep -q "agent updated: v$prev"; then
    # A rollout is ASYNCHRONOUS - the old pod/task goes, the new image is pulled,
    # the new one becomes ready - so reading the version ONCE, the instant the
    # CLI reports the move, measures the rollout's SPEED rather than its outcome.
    # It duly failed on one leg while the other two passed the same assert.
    #
    # Only here, never before this branch: on Compose nothing rolls (the CLI
    # cannot recreate its own container), so a wait for a version that will
    # never change burned its full budget on every compose leg - 200s of pure
    # sleep on the run's critical path.
    for _ in $(seq 1 40); do
      now="$(agent_version)"
      [ "$now" = "$target" ] && break
      sleep 5
    done
    if [ "$now" != "$target" ]; then
      echo "--- update-jump FAIL - reported a move to $target but the agent answers v$now"
      sum "**update jump** ❌ - moved to $target, agent still v$now"; return 1
    fi
    echo "update-jump OK - $prev retargeted to $target and the rollout landed (agent now v$now)"
    sum "**plug update (release agent rolls: $prev → $target)** ✅"; return 0
  fi
  now="$(agent_version)"
  if printf '%s' "$out" | grep -q "cannot recreate its own container"; then
    if [ "$now" != "$prev" ]; then
      echo "--- update-jump FAIL - nothing should have rolled here, yet the agent answers v$now"
      sum "**update jump** ❌ - unexpected roll to v$now"; return 1
    fi
    echo "update-jump OK - $prev retargeted to $target, pulled it, and handed back the recreate (agent still v$now)"
    sum "**plug update (release agent retargets: $prev → $target)** ✅"; return 0
  fi
  echo "--- update-jump FAIL - named $target but neither rolled nor pulled"
  sum "**update jump** ❌ - decision without an action"; return 1
}
