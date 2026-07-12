#!/usr/bin/env bash
# Windows mesh e2e client — runs under Git Bash on windows-latest. Build plug.exe,
# fetch wintun.dll (its driver), wait for the cluster over the tailnet, then assert
# httpbin is reachable BY NAME through plug. Windows has no sudo; plug.exe holds the
# datapath in-process (WinTUN + NRPT) and the runner user is elevated enough to
# create the adapter. Cluster DNS is registry-based (NRPT + ".plug" suffix), not
# mDNSResponder, so the macOS interface-scoped-resolver issue does not apply here.
#
#   e2e-client-win.sh <cluster-tailnet-name> [port]
set -uo pipefail
peer="${1:?usage: e2e-client-win.sh <cluster-tailnet-name> [port]}"
port="${2:-2222}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root/cli"

echo "=== build plug.exe ==="
go build -o plug.exe .

echo "=== fetch wintun.dll ==="
curl -sL https://www.wintun.net/builds/wintun-0.14.1.zip -o wintun.zip
powershell -NoProfile -Command "Expand-Archive -Path wintun.zip -DestinationPath wtun -Force"
cp wtun/wintun/bin/amd64/wintun.dll .

echo "=== wait for cluster $peer:$port over the tailnet ==="
ip=""
for _ in $(seq 1 60); do
  ip="$(tailscale ip -4 "$peer" 2>/dev/null | head -1 || true)"
  if [ -n "$ip" ] && ./plug.exe test --host "$ip" --port "$port" >/dev/null 2>&1; then
    echo "cluster reachable at $ip:$port"
    break
  fi
  ip=""; sleep 3
done
[ -n "$ip" ] || { echo "cluster $peer never became reachable" >&2; exit 1; }

echo "=== plug: reach httpbin BY NAME through the cluster ==="
if ./plug.exe --host "$ip" --port "$port" \
      curl -sf -m 25 -o /dev/null -w 'HTTP %{http_code}\n' http://httpbin:8080/get; then
  echo "E2E-MESH-OK — httpbin reached by name over the mesh (windows)"
else
  rc=$?
  echo "E2E-MESH-FAIL (rc $rc)" >&2
  # Diagnostics for a regression: DNS/adapter state under plug.
  ./plug.exe --host "$ip" --port "$port" cmd //c "nslookup httpbin & ipconfig /all" 2>&1 | head -40 || true
  exit 1
fi
