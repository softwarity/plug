#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# multicluster. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# multicluster: two clusters up at once (A and B, each `ident` answers its own
# id). The SAME name must reach the RIGHT backend through each plug, SIMULTANEOUSLY.
do_multicluster() {
  echo "=== multicluster: http://ident:5678 through plug-A and plug-B ==="
  # ident answers the corr id - strip whichever family prefix this leg targets
  # (plug-compose-<corr>, plug-k8s-<corr>, plug-swarm-<corr>: one rule, see
  # _cluster.yml).
  local expect_a="${peer#plug-compose-}" expect_b="${peer_b#plug-compose-}"
  expect_a="${expect_a#plug-k8s-}"; expect_b="${expect_b#plug-k8s-}"
  expect_a="${expect_a#plug-swarm-}"; expect_b="${expect_b#plug-swarm-}"
  local ip_b mc=PASS a_out="" b_out="" mc_pid
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b never became reachable" >&2; sum "**multicluster** ❌ (cluster B unreachable)"; return 1; }
  echo "cluster B reachable at $ip_b:$port"
  # An agent answering is not a cluster ready. wait_cluster proves the AGENT is
  # up, which on kind happens well before the deployments behind it are: the
  # `ident` service can still be pulling while its agent is already serving.
  #
  # That gap is what this cell kept dying of, on whichever leg happened to start
  # first: cluster B up for three minutes, its agent answering, and `ident`
  # returning nothing. The retry it had was two tries five seconds apart, and it
  # could not be made longer, because the whole point is to reach B WHILE A is
  # still alive and A lives for eight seconds. So the waiting belongs here,
  # before the timed part, not inside it.
  for _who in "A:$ip" "B:$ip_b"; do
    _lbl="${_who%%:*}"; _cip="${_who#*:}"
    _ready=""
    for _ in $(seq 1 40); do
      if plug_to "$_cip" curl -sS --max-time 5 http://ident:5678 2>/dev/null | grep -q .; then
        _ready=1; break
      fi
      sleep 3
    done
    [ -n "$_ready" ] || {
      echo "--- multicluster FAIL - cluster $_lbl has an agent but its ident service never answered in 120s"
      sum "**multicluster** ❌ (cluster $_lbl services not up)"; return 1
    }
  done
  # BOTH plugs live at once, on every OS: Linux gives each launch a private
  # resolver; Windows' SYSTEM service and macOS' global daemon each hold one
  # tunnel per cluster and attribute every flow by PID at connect.
  : > /tmp/mc-a.out
  plug_to "$ip" bash -c "curl -s http://ident:5678 > /tmp/mc-a.out && sleep 8" 2>/tmp/mc-a.err &
  mc_pid=$!
  sleep 4 # let A establish and answer while it is still alive...
  # ...then hit B DURING A. Two tries: another leg's resilience cell may be
  # restarting B's agent right now (a ~3s blip by design).
  b_out="$(plug_to "$ip_b" curl -sS http://ident:5678 2>/tmp/mc-b.err || true)"
  if ! printf '%s' "$b_out" | grep -q .; then
    sleep 5
    b_out="$(plug_to "$ip_b" curl -sS http://ident:5678 2>>/tmp/mc-b.err || true)"
  fi
  wait_bg "$mc_pid" "the multicluster session" 90
  a_out="$(cat /tmp/mc-a.out 2>/dev/null || true)"
  case "$a_out" in *"$expect_a"*) : ;; *) mc=FAIL ;; esac
  case "$b_out" in *"$expect_b"*) : ;; *) mc=FAIL ;; esac
  if [ "$mc" = PASS ]; then
    echo "multicluster OK - A→$a_out · B→$b_out"; sum "**multicluster** ✅ - A→\`$expect_a\` · B→\`$expect_b\`"; return 0
  fi
  echo "--- multicluster FAIL - A said '${a_out:-<nothing>}' (want $expect_a), B said '${b_out:-<nothing>}' (want $expect_b)"
  echo "    --- plug-A stderr ---"; tail -8 /tmp/mc-a.err 2>/dev/null | sed 's/^/    /'
  echo "    --- plug-B stderr ---"; tail -8 /tmp/mc-b.err 2>/dev/null | sed 's/^/    /'
  sum "**multicluster** ❌ - A \`${a_out:-nothing}\` (want \`$expect_a\`) · B \`${b_out:-nothing}\` (want \`$expect_b\`)"
  return 1
}
