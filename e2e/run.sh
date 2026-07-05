#!/bin/bash
# Run the whole matrix locally (every protocol), or a subset:
#   bash e2e/run.sh                 # all protocols
#   bash e2e/run.sh http postgres   # a subset
# Each protocol runs as its own isolated up/assert/down (like the CI jobs).
set -u
here="$(cd "$(dirname "$0")" && pwd)"
protos=("$@")
[ ${#protos[@]} -eq 0 ] && protos=(http postgres redis mongo amqp mqtt grpc)

rc=0
for p in "${protos[@]}"; do
  echo; echo "==================== $p ===================="
  bash "$here/matrix.sh" "$p" || rc=1
done
echo
[ "$rc" -eq 0 ] && echo "=== MATRIX OK ===" || echo "=== MATRIX FAILED ==="
exit "$rc"
