#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# update. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# update: `plug update` end to end against THIS LEG'S res-agent (per-leg, like
# resilience - never a shared agent). The agent side runs the docker backend:
# softwarity/plug:e2e exists only locally, so the pull fails cleanly and the
# verdict is `current … could not pull` - proving the verb answers and nothing
# is disturbed. The launcher side is a dev build facing a dev agent, so the
# self-replace path reports and skips (the rolling paths are bench-proven on
# kind/swarm). It runs after resilience, which crashes this same agent, hence
# the wait_agent below; the three update cells after it share that agent.
do_update() {
  local ragent="$res_agent" rsshport="$res_sshport"
  echo "=== update (cluster B): plug update against $ragent ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**plug update** ❌ (cluster B)"; return 1; }
  wait_agent "$ip_b" "$rsshport" || {
    echo "--- update FAIL - $rsshport never came back (the resilience cell crashes this agent)"
    agent_state "$ip_b" "$ragent"
    sum "**plug update** ❌ - per-leg agent never came back"; return 1
  }

  # Bounded, like every other plug call here: the cell watchdog is the last
  # resort, not the budget. 180s rather than the usual 60, because on compose
  # the agent PULLS the image before it answers.
  local out rc=0
  out="$(perl -e 'alarm 180; exec @ARGV or exit 127' "$PLUG" --host "$ip_b" --port "$rsshport" update </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  if [ "$rc" != 0 ]; then
    echo "--- update FAIL - exit $rc"; sum "**plug update** ❌ - exit $rc"; return 1
  fi
  # The verdict is backend-shaped, and all three are a working `update`:
  #   current  - already on the newest thing its tag can mean
  #   pulled   - Compose fetched it and handed back the recreate, since a
  #              container cannot recreate itself
  #   updating - Swarm rewrites the service image / k8s patches the Deployment,
  #              and the task rolls for real
  # The crash-test agents run a tag built INTO the cluster, with no registry
  # behind it, so on Swarm and k8s the roll lands on the same image and plug
  # says so rather than claiming a move. That report is the honest answer, not
  # a failure - what this cell asserts is that the verb ran, said which of the
  # three happened, and left the agent standing.
  if ! printf '%s' "$out" | grep -Eq "agent: (current|pulled|updating)"; then
    echo "--- update FAIL - no agent verdict (want current/pulled/updating)"; sum "**plug update** ❌ - no agent verdict"; return 1
  fi
  if ! printf '%s' "$out" | grep -Eq "launcher (already matches|is a dev build)"; then
    echo "--- update FAIL - no launcher line"; sum "**plug update** ❌ - no launcher line"; return 1
  fi
  # The verb must have left the agent standing.
  local v
  v="$(ssh_agent "$ip_b" "$rsshport" version 2>/dev/null | tr -d '\r')"
  if [ -z "$v" ]; then
    echo "--- update FAIL - $ragent no longer answers"; sum "**plug update** ❌ - agent down after"; return 1
  fi
  echo "update OK - verdict relayed, launcher path reported, $ragent still v$v"
  sum "**plug update (verb + launcher path)** ✅"; return 0
}
