#!/bin/bash
# End-to-end: build the agent from source, stand up a mini-cluster + a client
# "laptop", and assert plug makes cluster services reachable by name. Runs the
# same locally and in CI (ubuntu-latest, free for public repos).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"

echo "=== 1/3 build agent image from source ==="
docker build -f "$root/agent/Dockerfile" -t softwarity/plug:e2e "$root"

echo "=== 2/3 build + up (agent + web + client) ==="
cd "$here"
docker compose build
set +e
docker compose up --abort-on-container-exit --exit-code-from client
code=$?
set -e

echo "=== 3/3 teardown ==="
docker compose down -v --remove-orphans
echo "e2e exit code: $code"
exit "$code"
