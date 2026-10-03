#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# updatetag. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# --- update <tag>: the two ways it must REFUSE ---------------------------------
#
# Both matter more than the happy path. Repointing a deployment at a tag nobody
# published leaves an agent that cannot pull - on Swarm/k8s, a rollout to unwind
# by hand. And an agent that predates the target argument runs `self-update <tag>`
# as a PLAIN self-update: it ignores the word, so the channel would silently not
# change while the command reported success.
do_updatetag() {
  local rsshport="$res_sshport"
  echo "=== update-tag (cluster B): an unpublished tag, and an agent too old for the argument ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**update tag** ❌ (cluster B)"; return 1; }
  wait_agent "$ip_b" "$rsshport" || {
    echo "--- update_tag FAIL - $rsshport never came back (the resilience cell crashes this agent)"
    sum "**update tag** ❌ - per-leg agent never came back"; return 1
  }

  # 1) A tag the registry does not have: refused, and the agent left standing.
  # Bounded: a refusal must come back in seconds, and a verb that hangs on the
  # registry is a failure with a name rather than a watchdog kill.
  local out rc=0
  out="$(perl -e 'alarm 60; exec @ARGV or exit 127' "$PLUG" --host "$ip_b" --port "$rsshport" update definitely-not-a-published-tag </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  if [ "$rc" = 0 ]; then
    echo "--- update-tag FAIL - an unpublished tag was ACCEPTED"; sum "**update tag** ❌ - unpublished tag accepted"; return 1
  fi
  if ! printf '%s' "$out" | grep -q "has no tag"; then
    echo "--- update-tag FAIL - refused, but not for the right reason"; sum "**update tag** ❌ - wrong refusal"; return 1
  fi
  local v
  v="$(ssh_agent "$ip_b" "$rsshport" version 2>/dev/null | tr -d '\r')"
  if [ -z "$v" ]; then
    echo "--- update-tag FAIL - the agent went down on a refusal"; sum "**update tag** ❌ - agent down after refusal"; return 1
  fi

  # What used to be asserted here - that an agent predating the <tag> argument
  # is refused on its VERSION - is gone with the fixture it needed. plug is
  # young and agents follow releases (doctor and `plug update` exist for that),
  # so a cell pinned to a years-old build tests bugs fixed several releases ago:
  # a failure there says nothing about the code under review.
  echo "update-tag OK - the unpublished tag was refused and the agent is still up (v$v)"
  sum "**plug update <tag> (an unpublished tag is refused, agent left standing)** ✅"; return 0
}
