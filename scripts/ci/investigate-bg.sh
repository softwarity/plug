#!/usr/bin/env bash
# Reproduce, on a Mac, the takeover that fails while the cluster-B cells run
# beside the chain (scripts/ci/e2e-background.sh says what was seen), with
# every signal and exit on the machine recorded. Run by
# .github/workflows/investigate.yml, after the setup cell.
#
#   investigate-bg.sh <cluster-a> <cluster-b> [rounds]
#
# Starts the cluster-B cells beside, then runs the takeover cell against
# cluster A, round after round, while they run. Exits non-zero when a round
# failed: a red run is a reproduction.
set -uo pipefail
A="${1:?usage: investigate-bg.sh <cluster-a> <cluster-b> [rounds]}"
B="${2:?usage: investigate-bg.sh <cluster-a> <cluster-b> [rounds]}"
rounds="${3:-6}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
out="${RUNNER_TEMP:-/tmp}/investigate"
bgdir="${RUNNER_TEMP:-/tmp}/plug-e2e-bg"
mkdir -p "$out"
stamp() { date -u +%H:%M:%S; }

# Every signal sent on this machine, with its sender and its target, and every
# exit with its status: Endpoint Security, through eslogger (root, macOS 13+).
es_pid=""
if command -v eslogger >/dev/null 2>&1; then
  sudo eslogger signal exit > "$out/es.jsonl" 2> "$out/es.err" &
  es_pid=$!
  sleep 2
  kill -0 "$es_pid" 2>/dev/null || { echo "eslogger did not start:"; cat "$out/es.err"; es_pid=""; }
fi

echo "=== $(stamp) the cluster-B cells, beside ==="
PLUG_E2E_BG_ON_MAC=1 bash "$root/scripts/ci/e2e-background.sh" "$A" "$B" 2222

bg_done() { # all the cells beside have written their status
  local c
  for c in resilience admission update updatetag updatenotify; do
    [ -f "$bgdir/$c.rc" ] || return 1
  done
}
running_beside() {
  local c r=""
  for c in resilience admission update updatetag updatenotify; do
    [ -f "$bgdir/$c.rc" ] || { [ -s "$bgdir/$c.log" ] && r="$r $c"; }
  done
  echo "${r:- none}"
}

fails=0 ran=0
for i in $(seq 1 "$rounds"); do
  echo "=== $(stamp) round $i: takeover on cluster A; beside it:$(running_beside) ==="
  ran=$i
  if bash "$root/scripts/ci/e2e-matrix.sh" takeover "$A" "$B" 2222; then
    echo "=== $(stamp) round $i: OK ==="
  else
    fails=$((fails + 1))
    echo "=== $(stamp) round $i: FAIL ==="
    cp /tmp/takeover.out "$out/takeover-$i.out" 2>/dev/null
  fi
  bg_done && { echo "the cells beside are all done after round $i"; break; }
done

echo "=== $(stamp) waiting for the cells beside (5 min at most) ==="
for _ in $(seq 1 100); do bg_done && break; sleep 3; done
for c in resilience admission update updatetag updatenotify; do
  echo "--- $c: exit $(cat "$bgdir/$c.rc" 2>/dev/null || echo 'none (still running)'); its last lines:"
  tail -15 "$bgdir/$c.log" 2>/dev/null | sed 's/^/    /'
done

if [ -n "$es_pid" ]; then
  sudo kill "$es_pid" 2>/dev/null; sleep 1
  echo "=== the signals and exits of plug, its commands and the cells' shells (eslogger) ==="
  python3 "$root/scripts/ci/es-signals.py" "$out/es.jsonl" | tail -200
fi
echo "=== the daemon's account of the tunnels ==="
sudo grep -E "tunnel (up|down)|re-established|refused|stopping|reconcile|dropped" /var/run/plug/daemon.log 2>/dev/null | tail -80 | sed 's/^/    /'

echo "=== $fails failed round(s) out of $ran ==="
{
  echo "### Investigation: takeover beside the cluster-B cells on macOS"
  echo "$fails failed round(s) out of $ran"
} >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
[ "$fails" = 0 ]
