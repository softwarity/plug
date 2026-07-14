#!/usr/bin/env bash
# The launcher model's contract test, on EVERY OS: a user's INSTALLED launcher
# is old (from whenever they installed), while the cluster it talks to is NEW
# (this branch). The old launcher must still probe the agent, download THIS
# branch's core, and that core must find its cluster over the launcher→core env
# channel — the exact inter-version compat a PLUG_CORE_* rename silently broke
# once, and (on Linux) the ambient-caps privilege hand-off.
#
# The "old" launchers come from the published image (docker.io/softwarity/plug:
# latest = the last green main — slides forward on its own, no pinned release).
# mac/win runners have no Docker, so the start-cluster job extracts every
# OS/arch binary from that image into the "latest-launchers" artifact, which
# this script reads from ./latest-launchers/.
#
#   compat-launcher.sh <cluster-tailnet-name> [port]
set -uo pipefail
peer="${1:?usage: compat-launcher.sh <cluster-tailnet-name> [port]}"
port="${2:-2222}"

ip="$(tailscale ip -4 "$peer" | head -1)"
[ -n "$ip" ] || { echo "no tailnet ip for $peer" >&2; exit 1; }

# --- pick this OS/arch's launcher from the artifact; grant its REAL privilege ---
pre="" # command prefix for the launcher run
case "$(uname -s)" in
  Darwin)
    arch="$(uname -m)"; [ "$arch" = arm64 ] || arch=amd64
    L="./latest-launchers/plug-darwin-$arch"
    chmod +x "$L" 2>/dev/null || true
    # An installed mac launcher is a setuid-root helper; sudo gives the same
    # euid-0 start, and the core drops the child back to the user.
    pre="sudo"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    L="./latest-launchers/plug-windows-amd64.exe"
    # The versioned core looks for wintun.dll BESIDE the launcher — the installed
    # plug already dropped one in %LOCALAPPDATA%\Programs\plug during the grid.
    cp "$(cygpath "$LOCALAPPDATA")/Programs/plug/wintun.dll" ./latest-launchers/ 2>/dev/null || true
    # The datapath is the SYSTEM service, installed during the grid — no elevation
    # needed here (a non-elevated launcher starts the service via its ACL).
    ;;
  *)
    arch="$(uname -m)"; case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
    L="./latest-launchers/plug-linux-$arch"
    chmod +x "$L" 2>/dev/null || true
    # An installed Linux launcher carries file capabilities (setcap at install).
    sudo setcap cap_net_admin,cap_sys_admin,cap_net_bind_service+ep "$L"
    ;;
esac
[ -x "$L" ] || { echo "no launcher for $(uname -s)/$(uname -m) at $L" >&2; ls -la ./latest-launchers/ 2>/dev/null; exit 1; }

lver="$($L version)"
echo "latest launcher ($(uname -s)/${arch:-amd64}): v$lver"

# The privilege hand-off (Linux ambient caps) ships IN the launcher, so a
# published launcher that predates that fix can never pass the Linux leg — and
# the image gating would deadlock (red run → no publish → latest stays pre-fix
# forever). Arm the cell only once `latest` contains the fix; older launchers
# need one re-install (release-noted). Only the ambient-caps hand-off is
# version-sensitive, so this guard is Linux-only.
if [ "$(uname -s)" = Linux ]; then
  ambient_fix=0e8c6195a899418a70f5c8cd61efbfbfb0beeac2
  lrev="${lver##*+}" # "dev+<rev>" → <rev>
  if [ "$lrev" = "$lver" ] || ! git merge-base --is-ancestor "$ambient_fix" "$lrev" 2>/dev/null; then
    echo "latest launcher ($lver) predates the ambient-caps fix — Linux compat arms itself on the next publish"
    exit 0
  fi
fi

echo "=== old launcher → this branch's cluster (probe, download the new core, run) ==="
out="$(perl -e 'alarm 90; exec @ARGV or exit 127' $pre "$L" --host "$ip" --port "$port" curl -s http://httpbin:8080/get 2>&1 || true)"
printf '%s\n' "$out" | tail -8 | sed 's/^/    /'
if ! printf '%s' "$out" | grep -q '"url"'; then
  echo "COMPAT FAIL — the latest launcher could not run this branch's core against the cluster" >&2
  exit 1
fi
echo "compat OK — the latest launcher downloaded this branch's core and reached the cluster by name"
