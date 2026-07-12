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

# Probe every DNS layer UNDER plug (datapath active), to pinpoint the headless
# single-label failure precisely:
#   dig @198.18.0.53 httpbin      → does plug's DNS server answer the BARE name?
#   dig @198.18.0.53 httpbin.plug → does the ".plug" strip branch answer?
#   dscacheutil httpbin           → does macOS getaddrinfo route it to plug?
#   curl httpbin                  → the real assertion
echo "=== DNS probes UNDER plug (datapath active) ==="
$sudo ./plug --host "$ip" --port "$port" bash -c '
  echo "--- scutil --dns ---"; scutil --dns | sed -n "1,45p"
  echo "--- dig @198.18.0.53 httpbin (bare, plug DNS direct) ---"; dig +short +time=3 +tries=1 @198.18.0.53 httpbin || true
  echo "--- dig @198.18.0.53 httpbin.plug (suffixed) ---"; dig +short +time=3 +tries=1 @198.18.0.53 httpbin.plug || true
  echo "--- dscacheutil -q host httpbin (getaddrinfo path) ---"; dscacheutil -q host -a name httpbin || true
  echo "--- curl http://httpbin:8080 ---"; curl -sS -m 15 -o /dev/null -w "HTTP=%{http_code}\n" http://httpbin:8080/get || echo "curl exit $?"
' 2>&1 | tee probe.out

if grep -q 'HTTP=200' probe.out; then
  echo "E2E-MESH-OK — httpbin reached by name over the mesh"
else
  echo "E2E-MESH-FAIL" >&2
  exit 1
fi
