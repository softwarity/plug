# CI-only bake overlay, merged on top of compose.yml (run from the e2e/ dir):
#   docker buildx bake -f compose.yml -f bake-ci.hcl <targets>
#
# It does two things the plain `docker compose build` can't:
#   1. GitHub Actions layer cache (type=gha) on every image → unchanged deps are
#      restored from GitHub's cache instead of re-downloaded (no more Maven
#      Central 403s, and a faster build). mode=max also caches the discarded
#      multi-stage build layers (where mvn/npm/pip actually run).
#   2. Builds the agent image AND the clients in one graph: each client's
#      `FROM softwarity/plug:e2e` is wired to the freshly built `agentbase`
#      target via `contexts`, so no local image store is needed (which is what
#      lets the cache-capable container builder resolve it).
#
# `agentbase` is named differently from the compose `agent` service (which is
# image-only, no build) to avoid any target-name clash.

target "agentbase" {
  context    = ".."
  dockerfile = "agent/Dockerfile"
  tags       = ["softwarity/plug:e2e"]
  cache-from = ["type=gha,scope=agent"]
  cache-to   = ["type=gha,mode=max,scope=agent"]
}

target "grpc" {
  tags       = ["plug-e2e-grpc"]
  cache-from = ["type=gha,scope=grpc"]
  cache-to   = ["type=gha,mode=max,scope=grpc"]
}

target "client-go" {
  contexts   = { "softwarity/plug:e2e" = "target:agentbase" }
  tags       = ["plug-e2e-client-go"]
  cache-from = ["type=gha,scope=client-go"]
  cache-to   = ["type=gha,mode=max,scope=client-go"]
}

target "client-node" {
  contexts   = { "softwarity/plug:e2e" = "target:agentbase" }
  tags       = ["plug-e2e-client-node"]
  cache-from = ["type=gha,scope=client-node"]
  cache-to   = ["type=gha,mode=max,scope=client-node"]
}

target "client-python" {
  contexts   = { "softwarity/plug:e2e" = "target:agentbase" }
  tags       = ["plug-e2e-client-python"]
  cache-from = ["type=gha,scope=client-python"]
  cache-to   = ["type=gha,mode=max,scope=client-python"]
}

target "client-java" {
  contexts   = { "softwarity/plug:e2e" = "target:agentbase" }
  tags       = ["plug-e2e-client-java"]
  cache-from = ["type=gha,scope=client-java"]
  cache-to   = ["type=gha,mode=max,scope=client-java"]
}
