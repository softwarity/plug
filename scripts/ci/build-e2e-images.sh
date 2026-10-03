#!/usr/bin/env bash
# shellcheck shell=bash
# What the three cluster serve scripts (cluster-serve.sh, k8s-serve.sh,
# swarm-serve.sh) share before they stand their family up. SOURCED, not run:
# the previous release has to land in the caller's environment, where the
# fixtures substitute it.
#
#   require_agent_image      softwarity/plug:e2e must be loaded (the workflow
#                            pulled the published image, or built one on a
#                            manual dispatch) - this never builds it.
#   build_service_images     the six service images a single-node Swarm or a
#                            kind cluster takes from the local daemon (no
#                            registry): grpc, prober, flaky, gateway, chaos and
#                            the websocket server. Compose builds its own
#                            through `compose up --build`, so it does not call
#                            this.
#   resolve_previous_release exports PREV_RELEASE, the release the update
#                            cells start from. Resolved rather than pinned: a
#                            pinned tag re-tests bugs fixed several releases
#                            ago and takes the family down the day it leaves
#                            the registry (scripts/ci/previous-release.sh).
#
# Every function exits the caller on failure, as the inline code it replaces
# did under `set -e`.

e2e_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

require_agent_image() {
  docker image inspect softwarity/plug:e2e >/dev/null 2>&1 || {
    echo "softwarity/plug:e2e missing: the workflow's 'Pull the tested image' (or 'Load the image built here') step must run first" >&2
    exit 1
  }
}

build_service_images() {
  echo "=== build the local service images ==="
  local svc
  for svc in grpc prober flaky gateway chaos; do
    docker build -q -t "plug-e2e/$svc:e2e" -f "$e2e_root/e2e/services/$svc/Dockerfile" "$e2e_root/e2e" >/dev/null
  done
  docker build -q -t plug-e2e/wsserver:e2e -f "$e2e_root/e2e/services/websocket/Dockerfile" "$e2e_root/e2e" >/dev/null
}

resolve_previous_release() {
  PREV_RELEASE="$(bash "$e2e_root/scripts/ci/previous-release.sh")" || exit 1
  echo "previous release (update-cell agents) = $PREV_RELEASE"
  export PREV_RELEASE
}
