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

echo "=== build agent image ==="
docker build -f "$root/agent/Dockerfile" -t softwarity/plug:e2e "$root"

echo "=== up agent + services ==="
cd "$root/e2e"
compose="docker compose -f compose.yml -f compose.cluster.yml"
# MVP: just what the macOS http test needs. Add postgres/redis/... here as the
# matrix grows.
$compose up -d agent httpbin
$compose ps

echo "=== cluster up — serving for ${ttl}s (or until this run is cancelled) ==="
sleep "$ttl"
