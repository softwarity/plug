#!/bin/bash
# Runs inside the "client" container. Each case = run a client under plug and
# assert it reaches the cluster `web` service BY NAME. Exit non-zero if a
# REQUIRED case fails (XFAIL cases only warn).
set -u
AG=agent
PORT=22
fail=0

echo "waiting for agent sshd on $AG:$PORT ..."
for _ in $(seq 1 40); do (exec 3<>"/dev/tcp/$AG/$PORT") 2>/dev/null && { exec 3>&- 3<&-; break; }; sleep 1; done

hr() { echo; echo "### $1"; }

hr "CONTROL — curl web WITHOUT plug (web not on this network → must be unreachable)"
if curl -s --max-time 5 http://web:8080/ | grep -q CLUSTER-OK; then
  echo "  UNEXPECTED: reached web without plug"; fail=1
else
  echo "  ok — unreachable without plug (as designed)"
fi

hr "CASE 1 (required) — libc (curl) reaches cluster web BY NAME under plug"
out=$(PLUG_HOOK_DEBUG=1 plug -H "$AG" --port "$PORT" curl -sS --max-time 8 http://web:8080/ 2>&1)
echo "$out" | sed 's/^/    /'
if echo "$out" | grep -q CLUSTER-OK; then echo "  PASS ✅"; else echo "  FAIL ❌"; fail=1; fi

hr "CASE 2 (xfail on Linux) — Go raw-TCP reaches cluster web BY NAME under plug"
out=$(PLUG_HOOK_DEBUG=1 plug -H "$AG" --port "$PORT" goraw web:8080 2>&1)
echo "$out" | sed 's/^/    /'
if echo "$out" | grep -q CLUSTER-OK; then
  echo "  PASS ✅  (Go is covered!)"
else
  echo "  XFAIL — Go bypasses libc on Linux (seccomp supervisor pending); not a regression"
fi

echo
if [ "$fail" -eq 0 ]; then echo "=== E2E OK (required cases green) ==="; else echo "=== E2E FAILED ==="; fi
exit "$fail"
