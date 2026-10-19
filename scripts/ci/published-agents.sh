#!/usr/bin/env bash
# Deploy agents from the files the site offers for download, beside the e2e
# stack, for the "published" cell (scripts/ci/e2e/published.sh). Called by
# cluster-serve.sh (compose) and swarm-serve.sh (swarm) once their stack is up.
#
# The files are deployed as a reader deploys them, with two lines changed: the
# image, for this commit's, and the published port, which the e2e agent already
# holds at 2222. Kubernetes needs none of this: k8s-serve.sh deploys the chain's
# own agent from deploy/plug-k8s.yaml the same way.
#
#   compose: docs/snippets/plug-service.yml added to an app, on :2241
#   swarm:   the same snippet on :2241, and deploy/plug-stack.yml, the agent
#            for several stacks, on :2242
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
family="${1:?usage: published-agents.sh compose|swarm}"
work="$(mktemp -d)"

# rewrite <file> <port>: the file with its image and port changed, written
# under $work. sed substitutes nothing without a word when an anchor stops
# matching, and the cell would then run against the PUBLISHED image, so what
# came out is checked before anything is deployed (as k8s-serve.sh does).
rewrite() {
  local src="$1" port="$2" out
  out="$work/$(basename "$src")"
  sed -e 's|image: docker.io/softwarity/plug:latest|image: softwarity/plug:e2e|' \
      -e "s|\"2222:22\"|\"$port:22\"|" "$src" > "$out"
  local bad=""
  grep -qE '^[[:space:]]*image: softwarity/plug:e2e[[:space:]]*$' "$out" \
    || bad="no 'image: softwarity/plug:e2e' line"
  grep -qE "^[[:space:]]*- \"$port:22\"" "$out" \
    || bad="$bad${bad:+; }no '- \"$port:22\"' port"
  local left
  left="$(grep -nE '^[[:space:]]*image:' "$out" | grep -vE 'image: softwarity/plug:e2e[[:space:]]*$' || true)"
  [ -z "$left" ] || bad="$bad${bad:+; }an image left as published: $(tr '\n' ' ' <<<"$left")"
  if [ -n "$bad" ]; then
    echo "::error::$src no longer matches what scripts/ci/published-agents.sh rewrites ($bad). Unfixed, the published cell would test the released image, not this commit. Realign the sed anchors with the file." >&2
    exit 1
  fi
  echo "$out"
}

cd "$root/e2e"
case "$family" in
  compose)
    echo "=== the published snippet, added to an app (Compose, :2241) ==="
    docker network inspect app_net >/dev/null 2>&1 || docker network create app_net >/dev/null
    snippet="$(rewrite "$root/docs/snippets/plug-service.yml" 2241)"
    docker compose -p plug-pub -f published.app.yml -f "$snippet" up -d --build --wait
    docker compose -p plug-pub -f published.app.yml -f "$snippet" ps
    ;;
  swarm)
    echo "=== the published snippet, added to an app (Swarm, :2241), and deploy/plug-stack.yml (:2242) ==="
    docker network inspect app_net >/dev/null 2>&1 || docker network create -d overlay app_net >/dev/null
    snippet="$(rewrite "$root/docs/snippets/plug-service.yml" 2241)"
    stack="$(rewrite "$root/deploy/plug-stack.yml" 2242)"
    docker stack deploy --resolve-image never -c published.app.yml -c "$snippet" plug-pub
    docker stack deploy --resolve-image never -c "$stack" plug-stack
    lagging() {
      for s in plug-pub plug-stack; do
        docker stack services "$s" --format '{{.Name}} {{.Replicas}}' | awk -F'[ /]' '$2 != $3 {print $1}'
      done
    }
    ok=""
    for _ in $(seq 1 60); do
      if [ -z "$(lagging)" ]; then ok=1; break; fi
      sleep 5
    done
    docker stack services plug-pub; docker stack services plug-stack
    if [ -z "$ok" ]; then
      echo "=== NOT CONVERGED: the published agents ===" >&2
      docker stack ps plug-pub --no-trunc >&2 || true
      docker stack ps plug-stack --no-trunc >&2 || true
      for s in $(lagging); do docker service logs --tail 30 "$s" >&2 || true; done
      exit 1
    fi
    ;;
  *) echo "published-agents.sh: unknown family $family" >&2; exit 2 ;;
esac
