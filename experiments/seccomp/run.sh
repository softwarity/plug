#!/bin/bash
# Proof: a Go binary reaches a service BY NAME through seccomp — the name
# resolves to a fake IP, the supervisor traps the connect() and recovers the
# name (what the real path hands to SOCKS as remote DNS), then splices it.
set -u

echo "=== stand-in for plug's resolver: map a cluster NAME → fake IP in /etc/hosts ==="
echo "240.0.0.1 clustersvc" >>/etc/hosts
echo "  clustersvc → 240.0.0.1"
echo

echo "=== backend up ==="
BACKEND_ADDR=127.0.0.1:18080 ./backendsrv &
sleep 0.5
echo

echo "=== CONTROL: Go dials clustersvc WITHOUT supervisor (→ 240.0.0.1, unroutable → FAIL) ==="
if timeout 8 ./gotarget clustersvc:18080; then echo ">>> unexpected"; else echo ">>> failed as expected"; fi
echo

echo "=== TEST: same Go binary dials the cluster NAME UNDER the seccomp supervisor ==="
out=$(timeout 12 ./supervisor ./gotarget clustersvc:18080 2>&1)
echo "$out"
echo
if echo "$out" | grep -q "recovered cluster name 'clustersvc'" && echo "$out" | grep -q "SECCOMP-REDIRECT-OK"; then
  echo "###############################################################################"
  echo "#  PASS — a Go binary reached a service BY NAME via seccomp                    #"
  echo "#  name → fake IP → connect() trapped → NAME recovered (ready for SOCKS)       #"
  echo "###############################################################################"
  exit 0
else
  echo "XXXX FAIL"
  exit 1
fi
