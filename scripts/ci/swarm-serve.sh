#!/usr/bin/env bash
# Bring up the e2e "cluster", SWARM FLAVOR, on this runner and keep it alive:
# the Swarm twin of cluster-serve.sh / k8s-serve.sh, used by
# .github/workflows/swarm-for.yml (through _cluster.yml). A single-node swarm
# is initialized on the runner and the stack (e2e/swarm.cluster.yml) deployed
# on it: the agent runs as a real Swarm service on a non-attachable overlay,
# its SSH port published through the ingress, the same <tailnet-ip>:2222
# contract as the other families. A single-node swarm uses the node's local
# images: the service images are built here, no registry.
#
# Idles while the caller run lives (scripts/ci/idle-until-caller-done.sh),
# PLUG_CLUSTER_TTL being the backstop.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=scripts/ci/build-e2e-images.sh
. "$root/scripts/ci/build-e2e-images.sh"

require_agent_image

echo "=== swarm init (single node) ==="
if [ "$(docker info --format '{{.Swarm.LocalNodeState}}')" != "active" ]; then
  # 127.0.0.1: the runner has several interfaces (tailscale up) and no node
  # will ever join this swarm: pin the advertise addr so init can't dither.
  docker swarm init --advertise-addr 127.0.0.1 >/dev/null
fi
docker node ls

build_service_images
cd "$root/e2e"

echo "=== deploy the stack ==="
resolve_previous_release

# EXPORTED, not prefixed. This used to read
#
#   PLUG_CLUSTER_IDENT="..." \
#   # a comment
#     PREV_RELEASE=... docker stack deploy ...
#
# and the backslash continued the logical line INTO the comment, which swallowed
# the rest of it. What was meant as a command prefix became a bare shell
# assignment: the variable was set, never exported, and `docker stack deploy`
# substitutes from the ENVIRONMENT. So the swarm family deployed with an empty
# cluster identity while the kubernetes one, which prefixes envsubst properly,
# did not - and nothing failed, the ident service simply answered for nobody.
export PLUG_CLUSTER_IDENT="${PLUG_CLUSTER_IDENT:-solo}"
docker stack deploy --resolve-image never -c swarm.cluster.yml plug-e2e

echo "=== wait for convergence (every service at its full replica count) ==="
# 0/1 or 1/2 → not there yet; 1/1 and 2/2 are converged. awk, not a grep
# backreference: POSIX ERE has none (ugrep rejects it outright).
lagging() { docker stack services plug-e2e --format '{{.Name}} {{.Replicas}}' | awk -F'[ /]' '$2 != $3 {print $1}'; }
ok=""
for _ in $(seq 1 60); do
  if [ -z "$(lagging)" ]; then ok=1; break; fi
  sleep 5
done
docker stack services plug-e2e
if [ -z "$ok" ]; then
  echo "=== NOT CONVERGED: diagnostic dump ===" >&2
  docker stack ps plug-e2e --no-trunc >&2
  for s in $(lagging); do
    echo "--- service logs $s ---" >&2
    docker service logs --tail 30 "$s" >&2 || true
  done
  exit 1
fi

bash "$root/scripts/ci/idle-until-caller-done.sh"
