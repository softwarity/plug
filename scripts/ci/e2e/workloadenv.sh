#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# workload env: the other direction of env passthrough. A -s that takes a
# deployed service's place hands the command that service's environment, by
# default. tko-<os> carries two canaries nothing else has. Three assertions,
# one takeover each: the workload's variable reaches the command; a variable
# the caller set with the same key loses to the cluster; --no-env hands over
# nothing. Then --no-env=KEY (one key held back) and -c --env-of (a running
# workload read without parking it). Each session is bounded and ends by
# itself. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
do_workloadenv() {
  echo "=== workload env: the deployed service's environment reaches the command ==="
  # One target and one agent port PER LEG, not per OS: the arm64 leg is Linux
  # and runs on the same compose cluster as the ubuntu leg, concurrently. Keyed
  # by OS alone, both took tko-linux on agent port 18099 within the same
  # minute, the second takeover queued behind the first until its alarm killed
  # it, and the cell went red on both legs, with nothing to say. That cost the
  # publication run of 2.16.1. lib.sh keys tko_name/tko_port/tko_fwd on $leg.
  local tname="$tko_name" tport="$tko_port" tfwd="$tko_fwd"
  # Three takeovers of the same name, back to back. Each one parks tko-<os>
  # and its teardown restores it, and the next must not start until that
  # restore has landed: the first run of this cell had the first and third
  # sessions answer nothing at all, on the arm64 leg, because they hit the
  # previous session's teardown still in flight. takeover waits the same way.
  tko_probe() { prober_fetch "http://$tname:$tport/"; }
  restored() { # until the deployed service answers again, or give up after 30s
    retry_until 10 3 "deployed-$tname" tko_probe && return 0
    echo "--- workload env: $tname was not restored between sessions (prober said '${ru_out:-nothing}')"
    return 1
  }
  wenv() { # $1 = extra plug flags, $2 = a caller variable or "", prints DEPLOYED/SHARED as the child saw them
    restored || true
    # plug's own lines (took over, the agent's notes, N variable(s) given) go
    # to a file, printed only when the cell fails: three k8s legs answered
    # "unset" for every variable and the log held nothing to say why.
    env $2 perl -e 'alarm 60; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" $1 -s "$tname:$tfwd:$tfwd" \
      bash -c 'echo "${E2E_DEPLOYED:-unset}/${SHARED_KEY:-unset}/${FROM_SECRET:-unset}/$(cat "${CERT_FILE:-/none}" 2>/dev/null || echo unset)"' 2>>/tmp/wenv.err | tr -d '\r' | tail -1
  }
  : > /tmp/wenv.err
  local r1 r2 r3 r4 r5
  r1="$(wenv "" "")"
  r2="$(wenv "" "SHARED_KEY=from-the-caller")"
  r3="$(wenv "--no-env" "")"
  # The escape hatch: the cluster wins by DEFAULT (r2), but --no-env=SHARED_KEY
  # holds that one key back to the caller's own value while the rest still comes
  # from the cluster.
  r5="$(wenv "--no-env SHARED_KEY" "SHARED_KEY=from-the-caller")"
  # And without parking anything: a -c with --env-of reads the environment of
  # a workload that is RUNNING, found by its name rather than by a receipt.
  # The same three canaries, from the same service, with nothing taken over.
  restored || true
  r4="$(plug_bounded 60 --host "$ip" --port "$port" -c --env-of "$tname" \
    bash -c 'echo "${E2E_DEPLOYED:-unset}/${SHARED_KEY:-unset}/${FROM_SECRET:-unset}/$(cat "${CERT_FILE:-/none}" 2>/dev/null || echo unset)"' 2>>/tmp/wenv.err | tr -d '\r' | tail -1)"
  # FROM_SECRET is the one that proves the read is of the RUNNING process: on
  # Kubernetes it is a secretKeyRef, which the pod spec cannot answer, so a
  # collector that fell back to the spec (no pods/exec, or a handshake the API
  # server maps to the wrong verb) hands it over empty and this cell goes red.
  if [ "$r1" = "from-the-cluster/cluster-value/from-a-secret/from-a-file" ] && [ "$r2" = "from-the-cluster/cluster-value/from-a-secret/from-a-file" ] && [ "$r3" = "unset/unset/unset/unset" ] && [ "$r4" = "from-the-cluster/cluster-value/from-a-secret/from-a-file" ] && [ "$r5" = "from-the-cluster/from-the-caller/from-a-secret/from-a-file" ]; then
    echo "workload env OK: projected ($r1), the CLUSTER wins over the caller ($r2), --no-env hands over nothing ($r3), -c --env-of reads a running one ($r4), --no-env=SHARED_KEY keeps the caller value ($r5); the 4th field is a MOUNTED FILE, projected and read at its path"
    sum "**workload env (projected+file, cluster wins, --no-env, --no-env=KEY, -c --env-of)** ✅"
  else
    echo "--- workload env FAIL: projected='$r1' (want from-the-cluster/cluster-value/from-a-secret/from-a-file) cluster-wins='$r2' (want from-the-cluster/cluster-value/from-a-secret/from-a-file) no-env='$r3' (want unset/unset/unset/unset) env-of='$r4' (want from-the-cluster/cluster-value/from-a-secret/from-a-file) no-env-key='$r5' (want from-the-cluster/from-the-caller/from-a-secret/from-a-file)"
    echo "    --- what plug said across the four sessions ---"; grep -E "^\[plug\]" /tmp/wenv.err | grep -vE "using cluster version|serving |path verified|proving the path" | head -24 | sed "s/^/    /"
    sum "**workload env (projected+file, caller wins, --no-env, -c --env-of)** ❌: \`$r1\` · \`$r2\` · \`$r3\` · \`$r4\`"; return 1
  fi
}
