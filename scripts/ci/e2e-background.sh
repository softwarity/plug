#!/usr/bin/env bash
# Start the cells that run beside the rest of the leg ($bg_cells in
# scripts/ci/e2e/lib.sh, which says why they can), one after the other, in the
# background, and return at once.
#
#   e2e-background.sh <cluster-a> <cluster-b> [port]
#
# Each cell runs exactly as its own step would run it (e2e-matrix.sh, its
# watchdog included), into $bgdir/<cell>.log, its summary into
# $bgdir/<cell>.summary, its exit status into $bgdir/<cell>.rc. The step named
# after the cell later shows the log as it grows and ends with that status; a
# red cell here also stops the foreground cell that comes next, so the leg
# still fails in seconds rather than at the end. After a red one, the rest of
# them are not run, like the cells after a red step.
#
# Detached from the step entirely (no stdin, stdout or stderr in common): a
# step ends when its pipes close, so a child still holding them would keep this
# one open until the cells were done, which is the wait this exists to remove.
set -uo pipefail
# shellcheck disable=SC2034 # read by lib.sh
peer="${1:?usage: e2e-background.sh <cluster-a> <cluster-b> [port]}"
peer_b="${2:?usage: e2e-background.sh <cluster-a> <cluster-b> [port]}"
port="${3:-2222}"
phase=background
root="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=scripts/ci/e2e/lib.sh
. "$root/scripts/ci/e2e/lib.sh"
[ -f "$envfile" ] || { echo "no e2e state at $envfile - run the setup cell first" >&2; exit 1; }

rm -rf "$bgdir"; mkdir -p "$bgdir"
for c in $bg_cells; do : > "$bgdir/$c.queued"; done
(
  for c in $bg_cells; do
    if [ -s "$bgdir/failed" ]; then
      echo "not run: $(cat "$bgdir/failed") failed before it, beside the chain" > "$bgdir/$c.log"
      echo 1 > "$bgdir/$c.rc"
      continue
    fi
    PLUG_E2E_BACKGROUND=1 GITHUB_STEP_SUMMARY="$bgdir/$c.summary" \
      bash "$root/scripts/ci/e2e-matrix.sh" "$c" "$peer" "$peer_b" "$port" >"$bgdir/$c.log" 2>&1 </dev/null
    rc=$?
    [ "$rc" = 0 ] || echo "$c" > "$bgdir/failed"
    # Written last, through a rename: the join reads .rc as "the log is complete".
    echo "$rc" > "$bgdir/$c.rc.tmp" && mv "$bgdir/$c.rc.tmp" "$bgdir/$c.rc"
  done
) </dev/null >/dev/null 2>&1 &
echo "started beside the chain, in this order: $bg_cells"
echo "each one's output and verdict is in the step named after it"
