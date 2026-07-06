#!/usr/bin/env bash
# Render the platform-coverage table from the per-OS markers written by the CI
# jobs (test-<os>.txt = job status, selftest-<os>.txt = PASS/FAIL). In CI it
# appends to the job summary; run locally it prints to stdout.
#
#   bash scripts/platform-grid.sh [markers-dir]   # default: ./plat
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

out "## Platform coverage — plug built + the real TUN exercised on each OS"
out ""
out "| Platform | Build + unit tests | TUN selftest (real device, by name) |"
out "|---|---|---|"
out "| 🍎 macOS   | $(cell test-macos-latest.txt)   | $(cell selftest-macos-latest.txt)   |"
out "| 🪟 Windows | $(cell test-windows-latest.txt) | $(cell selftest-windows-latest.txt) |"
out "| 🐧 Linux   | $(cell test-ubuntu-latest.txt)  | $(cell selftest-ubuntu-latest.txt)  |"
out ""
out "_TUN selftest = a real utun / WinTUN / tun device, traffic looped BY NAME. Windows is WIP (driver loads; the 240/4 route config isn't done). The e2e protocol matrix (7 services × 4 languages) is Linux-only — Docker._"
