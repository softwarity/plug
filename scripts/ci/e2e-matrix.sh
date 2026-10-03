#!/usr/bin/env bash
# The mesh e2e, one CELL per CI step (its own green/red in the run view, no
# "e2e failed, go dig the log") sharing ONE install. Runs NATIVELY on this
# runner (Linux, macOS, or Windows via Git Bash): plug is INSTALLED FROM THE
# CLUSTER (the real one-liner, real privilege grant), then every cell runs that
# installed plug against a REAL cluster BY NAME over the Tailscale mesh.
#
#   e2e-matrix.sh <cell> <cluster-a> <cluster-b> [port]
#
# A cell is a file, scripts/ci/e2e/<cell>.sh, defining do_<cell>; the list is
# the directory, and the ORDER the three families run them in is written once,
# in .github/workflows/_e2e.yml. scripts/ci/e2e/lib.sh holds what the cells
# share (the OS table, the plug wrappers, assert_all, retry_until, the bounded
# waits, the exit trap).
#
# `setup` installs plug + builds the clients and records the shared state
# ($RUNNER_TEMP/plug-e2e-env) the other cells read back - they run as separate
# steps (separate shells), so nothing but files and that env file survives
# between them. Portable to macOS's bash 3.2: no associative arrays.
set -uo pipefail
phase="${1:?usage: e2e-matrix.sh <cell> <cluster-a> <cluster-b> [port]}"
# shellcheck disable=SC2034 # peer, peer_b and port are read by lib.sh and the cells
peer="${2:?usage: e2e-matrix.sh <cell> <cluster-a> <cluster-b> [port]}"
peer_b="${3:?usage: e2e-matrix.sh <cell> <cluster-a> <cluster-b> [port]}"
port="${4:-2222}"
root="$(cd "$(dirname "$0")/../.." && pwd)"

cell="$root/scripts/ci/e2e/$phase.sh"
if [ "$phase" = lib ] || [ ! -f "$cell" ]; then
  echo "unknown cell: $phase (one of: $(ls "$root/scripts/ci/e2e" | sed 's/\.sh$//' | grep -v '^lib$' | tr '\n' ' '))" >&2
  exit 2
fi

# shellcheck source=scripts/ci/e2e/lib.sh
. "$root/scripts/ci/e2e/lib.sh"

if [ "$phase" != setup ]; then
  [ -f "$envfile" ] || { echo "no e2e state at $envfile - run the setup cell first" >&2; exit 1; }
  # shellcheck disable=SC1090 # written by setup: PLUG, ip, built, job_started
  . "$envfile"
  { [ -n "${PLUG:-}" ] && [ -x "$PLUG" ]; } || { echo "plug not usable ('${PLUG:-}') - did setup fail?" >&2; exit 1; }
fi

# A cell that hangs must say so, not vanish.
#
# Everything INSIDE a cell is bounded already: plug sessions carry an alarm,
# waits go through wait_bg, every curl has --max-time. The cell itself was not,
# and that is the shape the failure keeps taking: a Windows leg sat in one cell
# for thirty-five minutes, the runner cancelled the job, and the log ended
# mid-cell with the ones after it never run and nothing saying which was to
# blame. It has happened on three different cells now, so the answer belongs here
# rather than in whichever cell it lands on next.
#
# Generous on purpose. The slowest cell finishes in about two minutes; twelve is
# not a performance budget, it is the line past which the cell is not slow, it is
# stuck. Crossing it prints what the shell was doing and kills the leg, which
# turns a silent cancellation into a failure with a name on it.
#
# perl's alarm rather than `timeout`: this runs on Git Bash too, and the harness
# already relies on perl being there for the per-session bound.
#
# AND IT IS CAPPED BY WHAT IS LEFT OF THE JOB, which the fixed budget was not -
# a calibration error that made this whole guard useless exactly where it was
# needed. A leg gets 25 or 30 minutes; the update cells close the chain and
# start around minute twenty, so a twelve-minute alarm there would ring at
# minute thirty-two, after GitHub has killed the job. A killed job takes its
# log with it, so the diagnosis this watchdog had just printed died with it: the
# flake has now cost three runs and left `log not found` every time.
#
# So a late cell gets a SHORT alarm rather than none: whatever is left before the
# job's own timeout, less a minute for the kill and the summary to be written
# while the runner is still alive. A cell with no room left keeps a floor of 60s,
# because a watchdog that fires immediately would fail healthy cells.
#
# "What is left" is counted from PLUG_JOB_T0, the job's FIRST step (_e2e.yml
# writes it into GITHUB_ENV before the checkout), and not from the end of
# setup: setup itself holds wait_cluster (up to ten minutes), the install and
# the wait for the services, and a clock started after them overstated the room
# a late cell had by exactly that much. The env file's job_started is the same
# value when setup ran under CI, and the setup-time fallback otherwise.
CELL_MAX="${PLUG_CELL_MAX:-720}"
job_t0="${PLUG_JOB_T0:-${job_started:-}}"
if [ -n "$job_t0" ] && [ -n "${PLUG_JOB_MINUTES:-}" ]; then
  cell_left=$(( job_t0 + PLUG_JOB_MINUTES * 60 - $(date +%s) - 60 ))
  [ "$cell_left" -lt 60 ] && cell_left=60
  [ "$cell_left" -lt "$CELL_MAX" ] && CELL_MAX="$cell_left"
fi

if [ "$phase" != setup ]; then
  ( sleep "$CELL_MAX"
    echo "::error::the '$phase' cell has been running for ${CELL_MAX}s and is not slow, it is stuck." >&2
    echo "--- what this shell was doing ---" >&2
    # shellcheck disable=SC2009 # pgrep is not on Git Bash, and the elapsed time
    # and full argv are the whole point: which session, started when.
    #
    # `plugin` is excluded because -A now returns the whole machine: a macOS
    # runner answers PlugInLibraryService and half a dozen audio plug-ins to a
    # case-insensitive `plug`, 94 lines of them here against 9 real ones, and
    # head -20 would show the noise instead of the session.
    ps_all | grep -iE "plug|sink|echo-local" | grep -viE "plugin" | grep -v grep | head -20 >&2
    for f in /tmp/gw.out /tmp/serve.out /tmp/takeover.out /tmp/resilience.out; do
      [ -s "$f" ] && { echo "--- $f ---" >&2; tail -8 "$f" >&2; }
    done
    # The TREE, not the shell alone, and this is the half that was missing.
    #
    # Killing $$ ends the script and nothing else. The children it leaves behind
    # still hold the step's stdout, and a step ends when its pipe closes, not
    # when its shell dies - so the step ran on until the JOB hit its 30 minutes,
    # and GitHub discards the log of a timed-out job, taking this diagnosis down
    # with it. That is how three hangs ended with no log at all, including the
    # one that had already been caught here and had nothing to show for it.
    # Measured on a bench: killing the shell alone let the step run 60s past the
    # watchdog, cutting the tree ended it in 1.
    #
    # Depth-first, so a child is gone before its parent: kill the parent first
    # and its children are reparented to init, out of reach of a walk that only
    # descends. The watchdog excludes ITSELF - it is a child of the shell it is
    # killing, and an earlier attempt cut its own branch before finishing.
    wd_me=$BASHPID
    kill_tree() {
      for kt_c in $(ps_all | awk -v p="$1" '$2==p {print $1}'); do
        [ "$kt_c" = "$wd_me" ] || kill_tree "$kt_c"
      done
      [ "$1" = "$wd_me" ] || kill -9 "$1" 2>/dev/null
    }
    kill_tree "$$" ) &
  cell_watchdog=$!
  # Disowned, so the shell stops tracking it as a job. Without this, killing it on
  # the way out makes bash announce "Terminated: 15 ( sleep ..." in the log of
  # every cell that passed, which is noise in exactly the place someone reads when
  # something failed.
  disown "$cell_watchdog" 2>/dev/null || true
  # Cleaned up by the EXIT trap lib.sh set, which already exists. Setting a
  # second one here would have REPLACED it, and with it the line that names the
  # signal a killed shell left on, which is the only lead those failures ever gave.
fi

# shellcheck disable=SC1090 # the cell named on the command line
. "$cell"
case "$phase" in
  # The last cell of the chain closes the leg's summary with the count of what
  # was skipped (see skip_cell), whatever its own verdict.
  updatejump) do_updatejump; rc=$?; leg_tally; exit "$rc" ;;
  *)          "do_$phase" ;;
esac
