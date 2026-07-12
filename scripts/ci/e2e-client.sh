#!/usr/bin/env bash
# Run the mesh e2e from THIS runner (macOS for now): build plug, wait for the
# cluster to be reachable over the tailnet, then assert a cluster service is
# reachable BY NAME through plug. Used by .github/workflows/e2e-mesh.yml.
#
#   e2e-client.sh <cluster-tailnet-name> [port]
#
# We resolve the cluster's tailnet IP with `tailscale ip` rather than relying on
# MagicDNS resolution under sudo (more robust). plug runs under sudo to open the
# utun, exactly like scripts/selftest.sh.
set -uo pipefail
peer="${1:?usage: e2e-client.sh <cluster-tailnet-name> [port]}"
port="${2:-2222}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root/cli"

echo "=== build plug ==="
go build -o plug .

echo "=== wait for cluster $peer:$port over the tailnet ==="
ip=""
for _ in $(seq 1 60); do
  ip="$(tailscale ip -4 "$peer" 2>/dev/null | head -1 || true)"
  if [ -n "$ip" ] && nc -z -w2 "$ip" "$port" 2>/dev/null; then
    echo "cluster reachable at $ip:$port"
    break
  fi
  ip=""; sleep 3
done
[ -n "$ip" ] || { echo "cluster $peer never became reachable" >&2; exit 1; }

sudo=""; [ "$(id -u)" = 0 ] || sudo=sudo

echo "=== agent reachable via $ip ==="
./plug test --host "$ip" --port "$port" 2>&1 || echo "(plug test returned $?)"

# The assertion: reach httpbin BY NAME through plug's datapath, over the mesh.
echo "=== plug: reach httpbin BY NAME through the cluster ==="
if $sudo ./plug --host "$ip" --port "$port" \
      curl -sf -m 25 -o /dev/null -w 'HTTP %{http_code}\n' http://httpbin:8080/get; then
  echo "E2E-MESH-OK — httpbin reached by name over the mesh"
else
  rc=$?
  echo "E2E-MESH-FAIL (rc $rc)" >&2
  # Dump the DNS state under plug to make a regression diagnosable next time.
  $sudo ./plug --host "$ip" --port "$port" bash -c '
    scutil --dns | sed -n "1,20p"
    dscacheutil -q host -a name httpbin
    dig +short +time=3 +tries=1 @198.18.0.53 httpbin
  ' 2>&1 | head -30 || true
  exit 1
fi
