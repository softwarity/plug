#!/usr/bin/env bash
# Build plug and run its TUN self-test on THIS OS: loop real traffic through a
# real device (utun / WinTUN / tun) BY NAME, with no agent and no Docker. Records
# a PASS/FAIL marker (read back by scripts/platform-grid.sh) and, in CI, appends a
# line to the job summary.
#
# Run it locally to exercise the datapath on your own machine (needs root for the
# device + routes):
#   sudo bash scripts/selftest.sh        # macOS / Linux
#
# Env: PLAT_OS overrides the marker's OS label (CI passes the runner name).
set -u
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/cli"

ext=""; sudo=""
case "$(uname -s)" in
  Linux|Darwin) os="$(uname -s)"; [ "$(id -u)" = 0 ] || sudo=sudo ;;
  MINGW*|MSYS*|CYGWIN*) os=Windows; ext=".exe" ;;
  *) echo "selftest: unsupported OS $(uname -s)"; exit 2 ;;
esac
label="${PLAT_OS:-$os}"

echo "=== build plug ==="
go build -o "plug$ext" .

# Windows: WinTUN needs its DLL next to the binary (extract via PowerShell — the
# runner's git-bash has no unzip).
if [ "$os" = Windows ]; then
  curl -sL https://www.wintun.net/builds/wintun-0.14.1.zip -o wintun.zip
  powershell -Command "Expand-Archive -Path wintun.zip -DestinationPath wtun -Force"
  cp wtun/wintun/bin/amd64/wintun.dll .
fi

echo "=== plug selftest ($label) ==="
if $sudo "./plug$ext" selftest 2>&1 | tee out.txt && grep -q "SELFTEST-OK" out.txt; then
  result=PASS; mark="✅ real device, traffic round-tripped by name"
else
  result=FAIL; mark="❌ (see log)"
fi

mkdir -p plat
printf '%s' "$result" > "plat/selftest-$label.txt"

line="### TUN selftest — $label $mark"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then echo "$line" >> "$GITHUB_STEP_SUMMARY"; else echo "$line"; fi

[ "$result" = PASS ]
