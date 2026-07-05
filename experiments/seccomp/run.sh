#!/bin/bash
# Proof (self-contained): a Go binary reaches a cluster service BY NAME through
# seccomp — the name is resolved by the supervisor's OWN embedded DNS (which
# mints a fake IP), then the connect() to that fake IP is trapped and the name
# recovered (ready for SOCKS remote-DNS). No /etc/hosts, no libc hook.
set -u

echo "=== point the child's resolver at our embedded DNS (stands in for plug wiring resolv.conf) ==="
echo "nameserver 127.0.0.1" >/etc/resolv.conf
cat /etc/resolv.conf
echo

echo "=== backend up ==="
BACKEND_ADDR=127.0.0.1:18080 ./backendsrv &
sleep 0.5
echo

echo "=== CONTROL: Go dials clustersvc WITHOUT supervisor (no embedded resolver → must FAIL) ==="
if timeout 8 ./gotarget clustersvc:18080; then echo ">>> unexpected"; else echo ">>> failed as expected"; fi
echo

echo "=== TEST: same Go binary dials the cluster NAME UNDER the supervisor (resolver + connect trap) ==="
out=$(timeout 15 ./supervisor ./gotarget clustersvc:18080 2>&1)
echo "$out"
echo
if echo "$out" | grep -q "cluster name 'clustersvc'" && echo "$out" | grep -q "SECCOMP-REDIRECT-OK"; then
  echo "###############################################################################"
  echo "#  PASS — Go reached a service BY NAME, fully self-contained (rootless-ready)  #"
  echo "#  embedded resolver → fake IP → connect() trapped → NAME recovered for SOCKS  #"
  echo "###############################################################################"
  exit 0
else
  echo "XXXX FAIL"
  exit 1
fi
