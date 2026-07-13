#!/usr/bin/env bash
# The launcher model's contract test: a user's INSTALLED launcher is old (from
# whenever they installed), while the cluster it talks to is NEW (this branch).
# The old launcher must still probe the agent, download THIS branch's core, and
# that core must find its cluster over the launcher→core env channel — exactly
# the inter-version compat a PLUG_CORE_* rename silently broke once (caught only
# by a local repro; the normal e2e always runs launcher == core).
#
# The "old" launcher comes from the published image (docker.io/softwarity/plug:
# latest = the last green main), so the test slides forward on its own — no
# pinned release to maintain.
#
#   compat-launcher.sh <cluster-tailnet-name> [port]      (Linux runner only)
set -euo pipefail
peer="${1:?usage: compat-launcher.sh <cluster-tailnet-name> [port]}"
port="${2:-2222}"

ip="$(tailscale ip -4 "$peer" | head -1)"
[ -n "$ip" ] || { echo "no tailnet ip for $peer" >&2; exit 1; }

echo "=== extract the LATEST published launcher (docker.io/softwarity/plug:latest) ==="
docker create --name plug-latest docker.io/softwarity/plug:latest >/dev/null
docker cp -q plug-latest:/opt/plug/bin/plug-linux-amd64 ./plug-latest-launcher
docker rm plug-latest >/dev/null
chmod +x ./plug-latest-launcher
sudo setcap cap_net_admin,cap_sys_admin,cap_net_bind_service+ep ./plug-latest-launcher
lver="$(./plug-latest-launcher version)"
echo "latest launcher: v$lver"

# The privilege hand-off (ambient caps) ships IN the launcher, so a published
# launcher that predates that fix can never pass this cell — and the image
# gating would deadlock (red run → no publish → latest stays pre-fix forever).
# Arm the cell only once `latest` contains the fix; launchers installed before
# it need one re-install (release-noted), which no future commit can avoid.
ambient_fix=0e8c6195a899418a70f5c8cd61efbfbfb0beeac2
lrev="${lver##*+}" # "dev+<rev>" → <rev>
if [ "$lrev" = "$lver" ] || ! git merge-base --is-ancestor "$ambient_fix" "$lrev" 2>/dev/null; then
  echo "latest launcher ($lver) predates the ambient-caps fix — compat cell arms itself on the next publish"
  exit 0
fi

echo "=== old launcher → this branch's cluster (probe, download the new core, run) ==="
out="$(perl -e 'alarm 90; exec @ARGV or exit 127' ./plug-latest-launcher --host "$ip" --port "$port" curl -s http://httpbin:8080/get 2>&1 || true)"
printf '%s\n' "$out" | tail -6 | sed 's/^/    /'
if ! printf '%s' "$out" | grep -q '"url"'; then
  echo "COMPAT FAIL — the latest launcher could not run this branch's core against the cluster" >&2
  exit 1
fi
echo "compat OK — the latest launcher downloaded this branch's core and reached the cluster by name"
