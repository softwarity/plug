#!/usr/bin/env bash
# Render the platform-coverage table from the per-OS markers written by the CI
# jobs (test-<os>.txt = job status, selftest-<os>.txt = PASS/FAIL). In CI it
# appends to the job summary; run locally it prints to stdout.
#
#   bash scripts/platform-grid.sh [markers-dir] [--strict]   # default dir: ./plat
#
# It also emits the VERDICT the publication gates read (`verdict=pass|fail` on
# $GITHUB_OUTPUT). That verdict exists because job statuses cannot be trusted
# here: the Windows selftest is continue-on-error, so a genuine FAIL leaves its
# job green — which is how the image of 33f8c40 shipped with a red selftest. The
# markers are the only honest record, so both gates read THESE.
#
# --strict additionally exits non-zero on a fail verdict (what a release gate
# wants); without it the script only reports, so rendering the grid never turns
# a tolerated Windows failure into a red CI run.
set -u
dir="${1:-plat}"
strict=""
[ "${2:-}" = "--strict" ] && strict=1

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

out "## Platform coverage — plug built + the real TUN exercised on each OS"
out ""
out "| Platform | Build + unit tests | TUN selftest (real device, by name) |"
out "|---|---|---|"
out "| 🍎 macOS   | $(cell test-macos-latest.txt)   | $(cell selftest-macos-latest.txt)   |"
out "| 🪟 Windows | $(cell test-windows-latest.txt) | $(cell selftest-windows-latest.txt) |"
out "| 🐧 Linux   | $(cell test-ubuntu-latest.txt)  | $(cell selftest-ubuntu-latest.txt)  |"
out ""
out "_TUN selftest = a real utun / WinTUN / tun device, traffic looped BY NAME, plus a fabricated VPN whose resolver plug must follow up and back down. The e2e protocol matrix (7 services × 4 languages) is Linux-only — Docker._"

# The verdict, from the same markers the grid just rendered. A MISSING marker is
# a fail, not a blank: it means a job never got to say how it went, and a gate
# that treats silence as consent is not a gate.
verdict=pass
bad=""
for m in test-macos-latest test-windows-latest test-ubuntu-latest \
  selftest-macos-latest selftest-windows-latest selftest-ubuntu-latest; do
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
  echo "platform verdict: fail —$bad"
  out ""
  out "> ❌ **Not publishable**: $bad. The image gate reads these markers, not the job statuses."
  [ -n "$strict" ] && { echo "::error::platform markers not green —$bad"; exit 1; }
fi
exit 0
