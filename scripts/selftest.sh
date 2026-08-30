#!/usr/bin/env bash
# Build plug and run its TUN self-test on THIS OS: loop real traffic through a
# real device (utun / WinTUN / tun) BY NAME, and follow a fabricated VPN's
# resolver up and back down, with no agent and no Docker. Records
# a PASS/FAIL marker (read back by scripts/platform-grid.sh) and, in CI, appends a
# line to the job summary.
#
# Run it locally to exercise the datapath on your own machine (needs root for the
# device + routes):
#   sudo bash scripts/selftest.sh        # macOS / Linux
#
# Env: PLAT_OS overrides the marker's OS label (CI passes the runner name).
# -e and pipefail are not decoration here. Without pipefail the verdict below
# reads the exit status of `tee`, so a plug that prints SELFTEST-OK and then
# panics is recorded PASS - and that marker is exactly what the publication
# barrier reads back (platform-grid.sh, ci.yml). Without -e, a failed `go build`
# does not stop anything, and the run tests whatever stale binary is lying
# around from an earlier build.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/cli"

ext=""; sudo=""
case "$(uname -s)" in
  Linux|Darwin) os="$(uname -s)"; [ "$(id -u)" = 0 ] || sudo=sudo ;;
  MINGW*|MSYS*|CYGWIN*) os=Windows; ext=".exe" ;;
  *) echo "selftest: unsupported OS $(uname -s)"; exit 2 ;;
esac
label="${PLAT_OS:-$os}"

# After $ext is known, and before the build: a stale binary from an earlier run
# (local, or a reused runner) must never be what gets tested, and a stale
# transcript must never be what the verdict reads.
rm -f "plug$ext" out.txt

echo "=== build plug ==="
go build -o "plug$ext" .

# Windows: WinTUN needs its DLL next to the binary (extract via PowerShell — the
# runner's git-bash has no unzip).
if [ "$os" = Windows ]; then
  # -f, so a 404 fails here instead of writing an HTML error page into the zip
  # and being discovered three commands later as a broken archive. The DLL is
  # loaded by a process running elevated, so what lands here matters.
  # Same digest, same reason as agent/Dockerfile: this DLL is loaded by a process
  # running elevated, and the selftest opens a real device with it. Kept in step
  # with the Dockerfile by hand, which is why both name the version beside it.
  wintun_sha=07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51
  curl -fsSL https://www.wintun.net/builds/wintun-0.14.1.zip -o wintun.zip
  echo "$wintun_sha  wintun.zip" | sha256sum -c - \
    || { echo "selftest: wintun.zip is not the archive this build expects"; exit 1; }
  powershell -Command "Expand-Archive -Path wintun.zip -DestinationPath wtun -Force"
  cp wtun/wintun/bin/amd64/wintun.dll .
fi

# The fake-VPN probe (opt-in, see tun.SelfTest): fabricate what a VPN does to
# this machine — an extra address carrying a resolver that knows a name nothing
# else knows, announced through the very door plug reads from on this OS — and
# assert plug follows it, and follows it back down. Set PLUG_SELFTEST_VPN=0 to
# skip it. Passed through `env` because sudo resets the environment.
echo "=== plug selftest ($label) ==="
if $sudo env PLUG_SELFTEST_VPN="${PLUG_SELFTEST_VPN:-1}" "./plug$ext" selftest 2>&1 | tee out.txt && grep -q "SELFTEST-OK" out.txt; then
  result=PASS; mark="✅ real device, traffic round-tripped by name"
else
  result=FAIL; mark="❌ (see log)"
fi

mkdir -p plat
printf '%s' "$result" > "plat/selftest-$label.txt"

line="### TUN selftest — $label $mark"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then echo "$line" >> "$GITHUB_STEP_SUMMARY"; else echo "$line"; fi

[ "$result" = PASS ]
