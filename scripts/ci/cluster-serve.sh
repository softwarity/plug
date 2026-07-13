#!/usr/bin/env bash
# Bring up the e2e "cluster" on THIS runner and keep it alive so a remote runner
# (macOS/Windows), joined to the same Tailscale tailnet, can reach it BY NAME
# through plug. Used by .github/workflows/cluster.yml.
#
# The agent publishes :2222 on the host (compose.cluster.yml) so the runner's
# tailnet IP:2222 lands on it. The services (httpbin, ...) stay on the internal
# network — only the agent can reach them, exactly like a real cluster.
#
# Idles for PLUG_CLUSTER_TTL seconds; the caller cancels this run earlier via
# `gh run cancel` once the e2e jobs are done (the TTL is just the safety net).
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
ttl="${PLUG_CLUSTER_TTL:-1800}"

# The agent image is built by the workflow's dedicated "Build the agent image"
# step (visible in the pipeline) — this script only serves it.
docker image inspect softwarity/plug:e2e >/dev/null 2>&1 || {
  echo "softwarity/plug:e2e missing — run the 'Build the agent image' step first" >&2
  exit 1
}

echo "=== up agent + all services ==="
cd "$root/e2e"
compose="docker compose -f compose.yml -f compose.cluster.yml"
# The full protocol matrix: one service per protocol, plus `ident` (answers this
# cluster's PLUG_CLUSTER_IDENT — the multicluster assert). grpc/wsserver are built
# (compose build); the rest are pulled images. --wait blocks on the healthchecks.
$compose up -d --build --wait \
  agent httpbin postgres redis mongo rabbitmq mosquitto grpc wsserver ident flaky
$compose ps

echo "=== cluster up — serving for ${ttl}s (or until this run is cancelled) ==="
sleep "$ttl"
