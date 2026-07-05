#!/bin/bash
# One protocol, end-to-end: bring up `agent` + the protocol's service, then run
# each language client UNDER plug against it BY NAME, and print a PASS/FAIL grid.
#
#   bash e2e/matrix.sh <proto>          # http | postgres | redis | mongo | amqp | mqtt | grpc
#   E2E_LANGS="go node" bash e2e/matrix.sh http   # subset of clients
#
# Exits non-zero if any client fails (this is the regression guard).
set -u
proto="${1:?usage: matrix.sh <proto>}"
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
cd "$here"

case "$proto" in
  http)     svc=httpbin;   target="httpbin:8080" ;;
  postgres) svc=postgres;  target="postgres:5432" ;;
  redis)    svc=redis;     target="redis:6379" ;;
  mongo)    svc=mongo;     target="mongo:27017" ;;
  amqp)     svc=rabbitmq;  target="rabbitmq:5672" ;;
  mqtt)     svc=mosquitto; target="mosquitto:1883" ;;
  grpc)     svc=grpc;      target="grpc:50051" ;;
  *) echo "unknown proto: $proto"; exit 2 ;;
esac

read -ra langs <<<"${E2E_LANGS:-go node python java}"
DC="docker compose -f compose.yml"
# E2E_BUILD=0 (CI, images preloaded via `docker load`) skips all builds.
build=""; [ "${E2E_BUILD:-1}" != "0" ] && build="--build"

# Agent image: build if absent (CI loads it from an artifact; local caches it).
# `docker rmi softwarity/plug:e2e` to force a rebuild after changing plug itself.
if ! docker image inspect softwarity/plug:e2e >/dev/null 2>&1; then
  echo "=== build agent image ==="
  docker build -q -f "$root/agent/Dockerfile" -t softwarity/plug:e2e "$root" >/dev/null
fi

echo "=== up: agent + $svc ==="
$DC up -d --wait $build agent "$svc"

echo "=== clients under plug → $proto $target ==="
# plug's single data path is the userspace TUN (needs NET_ADMIN + /dev/net/tun,
# set on the client services in compose.yml). No mode flag — plug always uses it.
declare -A result
for l in "${langs[@]}"; do
  out=$($DC run --rm $build "client-$l" "$proto" "$target" 2>&1)
  if echo "$out" | grep -q "E2E-OK"; then
    result[$l]=PASS
  else
    result[$l]=FAIL
    echo "--- $l ---"; echo "$out" | sed 's/^/    /'
  fi
done

# E2E_XFAIL (space-separated "lang:proto") lets a cell warn instead of failing.
# Empty by default: the TUN captures at the IP layer, so every runtime is covered
# — including gRPC on the JVM/CPython, which the old fd-level path could never do.
read -ra xfail <<<"${E2E_XFAIL-}"
is_xfail() { local c; for c in "${xfail[@]}"; do [ "$c" = "$1:$proto" ] && return 0; done; return 1; }

echo
echo "######## protocol=$proto  service=$svc ########"
fail=0
for l in "${langs[@]}"; do
  r="${result[$l]:-?}"
  if [ "$r" = PASS ]; then printf "  %-8s PASS ✅\n" "$l"
  elif is_xfail "$l"; then printf "  %-8s %s ⚠️  (xfail — known gap)\n" "$l" "$r"
  else printf "  %-8s %s ❌\n" "$l" "$r"; fail=1; fi
done
echo "###############################################"

# GitHub Actions: per-protocol grid into the job summary + a result line for the
# aggregate grid (read back by the `grid` job).
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### \`$proto\` — service \`$svc\`${E2E_MODE_LABEL:+ · ${E2E_MODE_LABEL} mode}"
    echo "| client | result |"
    echo "|---|---|"
    for l in "${langs[@]}"; do
      r="${result[$l]:-?}"
      if [ "$r" = PASS ]; then echo "| $l | ✅ PASS |"
      elif is_xfail "$l"; then echo "| $l | ⚠️ $r (known gap) |"
      else echo "| $l | ❌ $r |"; fi
    done
  } >> "$GITHUB_STEP_SUMMARY"
fi
if [ -n "${E2E_RESULT_DIR:-}" ]; then
  mkdir -p "$E2E_RESULT_DIR"
  # E2E_RESULT_TAG keeps per-mode results from colliding (e.g. tun-grpc vs rootless-grpc).
  line="$proto"; for l in "${langs[@]}"; do line="$line $l=${result[$l]:-?}"; done
  echo "$line" > "$E2E_RESULT_DIR/${E2E_RESULT_TAG:-$proto}.txt"
fi

$DC down -v --remove-orphans >/dev/null 2>&1
exit $fail
