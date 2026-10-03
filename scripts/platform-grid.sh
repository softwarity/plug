#!/usr/bin/env bash
# Render the platform-coverage table from the per-OS markers written by the CI
# jobs (test-<os>.txt = job status, selftest-<os>.txt = PASS/FAIL). In CI it
# appends to the job summary; run locally it prints to stdout.
#
#   bash scripts/platform-grid.sh [markers-dir]   # default dir: ./plat
#
# It also emits the VERDICT the image job reads (`verdict=pass|fail` on
# $GITHUB_OUTPUT). That verdict exists because `needs` cannot be trusted on
# its own: it is satisfied by a job that was skipped or cancelled, and it was
# satisfied by the Windows selftest while that job tolerated its own failure,
# which is how the image of 33f8c40 shipped with a red selftest. The markers
# are the only record of what each job actually did, so the gate reads THESE.
# A missing marker is a fail, not a blank.
#
# This script only reports: the verdict is acted on by the job that reads it,
# so rendering the grid never turns a red platform into a red `platforms` job
# that would hide the grid itself.
set -u
dir="${1:-plat}"

cell() {
  case "$(cat "$dir/$1" 2>/dev/null)" in
    PASS | success) echo "✅" ;;
    FAIL | failure) echo "❌" ;;
    *) echo "·" ;;
  esac
}
out() {
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then echo "$*" >> "$GITHUB_STEP_SUMMARY"; else echo "$*"; fi
}

out "## Platform coverage: plug built + the real TUN exercised on each OS"
out ""
out "| Platform | Build + unit tests | TUN selftest (real device, by name) |"
out "|---|---|---|"
out "| 🍎 macOS   | $(cell test-macos-latest.txt)   | $(cell selftest-macos-latest.txt)   |"
out "| 🪟 Windows | $(cell test-windows-latest.txt) | $(cell selftest-windows-latest.txt) |"
out "| 🪟 Windows arm64 | - | $(cell selftest-windows-11-arm.txt) |"
out "| 🐧 Linux   | $(cell test-ubuntu-latest.txt)  | $(cell selftest-ubuntu-latest.txt)  |"
out ""
out "_TUN selftest = a real utun / WinTUN / tun device, traffic looped BY NAME, plus a fabricated VPN whose resolver plug must follow up and back down. The e2e protocol matrix (8 protocols × 4 languages) runs natively on all three._"

# The verdict, from the same markers the grid just rendered. A MISSING marker is
# a fail, not a blank: it means a job never got to say how it went, and a gate
# that treats silence as consent is not a gate.
verdict=pass
bad=""
# windows-11-arm carries a selftest and no unit-test marker: the unit tests run
# on three OSes, and what arm64 adds is a REAL WinTUN device on a real arm
# kernel, which is the half that could differ. It is in this list, so a red one
# blocks publication like any other - a platform exercised but not gated is a
# platform nobody is watching.
for m in test-macos-latest test-windows-latest test-ubuntu-latest \
  selftest-macos-latest selftest-windows-latest selftest-ubuntu-latest \
  selftest-windows-11-arm; do
  case "$(cat "$dir/$m.txt" 2>/dev/null)" in
    PASS | success) ;;
    "") verdict=fail; bad="$bad $m(missing)" ;;
    *) verdict=fail; bad="$bad $m" ;;
  esac
done
[ -n "${GITHUB_OUTPUT:-}" ] && echo "verdict=$verdict" >> "$GITHUB_OUTPUT"
if [ "$verdict" = pass ]; then
  echo "platform verdict: pass"
else
  echo "platform verdict: fail:$bad"
  out ""
  out "> ❌ **Not publishable**: $bad. The image gate reads these markers, not the job statuses."
fi
exit 0
