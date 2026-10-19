#!/usr/bin/env bash
# Bring up the e2e "cluster", COMPOSE FLAVOR, on THIS runner and keep it alive
# so a remote runner (macOS/Windows), joined to the same Tailscale tailnet, can
# reach it BY NAME through plug. Used by .github/workflows/compose-for.yml
# (through _cluster.yml).
#
# The agent publishes :2222 on the host (compose.cluster.yml) so the runner's
# tailnet IP:2222 lands on it. The services (httpbin, ...) stay on the internal
# network: only the agent can reach them, exactly like a real cluster.
#
# Idles while the caller run lives (scripts/ci/idle-until-caller-done.sh),
# PLUG_CLUSTER_TTL being the backstop.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=scripts/ci/build-e2e-images.sh
. "$root/scripts/ci/build-e2e-images.sh"

cd "$root/e2e"
resolve_previous_release
compose="docker compose -f compose.yml -f compose.cluster.yml"

# Two phases (see "Prepare the cluster" in _cluster.yml): `prepare` builds the
# services compose builds, which does not need the tested image, while ci.yml
# is still building that image; the serve phase brings everything up (its
# --build then finds them built) and idles.
if [ "${1:-}" = prepare ]; then
  echo "=== build the services compose builds ==="
  $compose build grpc wsserver prober gateway flaky-linux flaky-mac flaky-win chaos
  exit 0
fi

# The agent image is what the workflow pulled (the one ci.yml published for
# this commit) or, on a manual dispatch, built: this script only serves it.
require_agent_image

echo "=== up agent + all services ==="
# The full protocol matrix: one service per protocol, plus `ident` (answers this
# cluster's PLUG_CLUSTER_IDENT, the multicluster assert). grpc/wsserver are built
# (compose build); the rest are pulled images. --wait blocks on the healthchecks.
$compose up -d --build --wait \
  agent httpbin postgres redis mongo rabbitmq mosquitto grpc wsserver ident \
  flaky-linux flaky-mac flaky-win tko-linux tko-mac tko-win tko-arm prober gateway \
  chaos res-tko-linux res-tko-mac res-tko-win \
  res-agent-linux res-agent-mac res-agent-win \
  prev-agent-linux prev-agent-mac prev-agent-win \
  adm-agent-linux adm-agent-mac adm-agent-win adm-echo \
  vol-linux vol-mac vol-win
$compose ps

# The agent as the site's download deploys it, for the published cell.
bash "$root/scripts/ci/published-agents.sh" compose

bash "$root/scripts/ci/idle-until-caller-done.sh"
