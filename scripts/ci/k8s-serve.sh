#!/usr/bin/env bash
# Bring up the e2e "cluster", KUBERNETES FLAVOR, on this runner and keep it
# alive — the k8s twin of cluster-serve.sh, used by .github/workflows/k8s-for.yml.
# A kind cluster stands in for the real thing: upstream Kubernetes in a Docker
# container, NodePorts published on the runner via e2e/kind-config.yaml, so a
# remote runner on the tailnet reaches the agent at <tailnet-ip>:2222 and the
# gateway at :18090 — the exact contract of the compose cluster.
#
# The agent is applied from deploy/plug-k8s.yaml — the PUBLISHED manifest
# (ServiceAccount, Services-only RBAC, NodePort) with only the image swapped to
# the branch-built :e2e — so every push blesses the file users deploy. The
# services come from e2e/k8s.cluster.yaml (same names/ports as compose).
#
# Idles for PLUG_CLUSTER_TTL seconds; the caller cancels the run earlier.
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
ttl="${PLUG_CLUSTER_TTL:-1800}"

docker image inspect softwarity/plug:e2e >/dev/null 2>&1 || {
  echo "softwarity/plug:e2e missing — run the 'Load the agent image' step first" >&2
  exit 1
}

# Pinned, checksummed and retried, because this binary is written as ROOT into
# /usr/local/bin and then run, and it sat on the critical path of the three k8s
# legs with none of the three.
#
# Pinned: `latest` meant an upstream release could change what kind-config.yaml
# means, or what --wait does, and turn the whole k8s family red on a day nobody
# touched this repository. Checksummed: the Dockerfile already does exactly this
# for the WinTUN driver, for exactly this reason, and kind was the one download
# that escaped it. Retried: a bad minute on the network must not redden a good
# commit, which is why go-mod-download.sh exists next door.
KIND_VERSION=v0.33.0
KIND_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
echo "=== install kind $KIND_VERSION ==="
curl -fsSL --retry 3 --retry-connrefused --retry-delay 2 -o /tmp/kind \
  "https://github.com/kubernetes-sigs/kind/releases/download/${KIND_VERSION}/kind-linux-amd64"
echo "$KIND_SHA256  /tmp/kind" | sha256sum -c - || {
  echo "::error::kind $KIND_VERSION does not match the digest this script pins. Either the download was" >&2
  echo "         corrupted, or the release was replaced upstream. Refusing to run it as root." >&2
  exit 1
}
sudo install -m 0755 /tmp/kind /usr/local/bin/kind
rm -f /tmp/kind
kind version

echo "=== create the kind cluster ==="
kind create cluster --config "$root/e2e/kind-config.yaml" --wait 120s

echo "=== build + load the local service images ==="
cd "$root/e2e"
for svc in grpc prober flaky gateway chaos; do
  docker build -q -t "plug-e2e/$svc:e2e" -f "services/$svc/Dockerfile" . >/dev/null
done
docker build -q -t plug-e2e/wsserver:e2e -f services/websocket/Dockerfile . >/dev/null
kind load docker-image softwarity/plug:e2e \
  plug-e2e/grpc:e2e plug-e2e/wsserver:e2e plug-e2e/prober:e2e \
  plug-e2e/flaky:e2e plug-e2e/gateway:e2e plug-e2e/chaos:e2e

echo "=== deploy the agent (the PUBLISHED manifest, branch image) ==="
# This rewrite is the only thing standing between the three k8s legs and a run
# that proves nothing about the branch. Piping sed straight into kubectl could
# not say whether it had done anything: sed exits 0 when it substitutes nothing,
# and the manifest's own comment says both of these lines are meant to change one
# day (a pinned tag instead of a moving `latest`, IfNotPresent instead of
# Always). The day they did, the anchors would stop matching, kind would deploy
# the PUBLISHED image, and the nine k8s legs would report green on code that was
# never loaded into the cluster. So: substitute into a variable, then assert what
# came out, before anything reaches the API server.
manifest="$(sed -e 's|image: docker.io/softwarity/plug:latest|image: softwarity/plug:e2e|' \
                -e 's|imagePullPolicy: Always|imagePullPolicy: Never|' \
                "$root/deploy/plug-k8s.yaml")"

rewrite_failed=""
grep -qE '^[[:space:]]*image: softwarity/plug:e2e([[:space:]]|$)' <<<"$manifest" \
  || rewrite_failed="the branch image line 'image: softwarity/plug:e2e' is absent"
grep -qE '^[[:space:]]*imagePullPolicy: Never([[:space:]]|$)' <<<"$manifest" \
  || rewrite_failed="$rewrite_failed${rewrite_failed:+; }'imagePullPolicy: Never' is absent"
# Nothing may still point at a registry image: a second container, a renamed tag,
# a digest pin. Any surviving reference is an image kind would PULL.
leftover="$(grep -nE '^[[:space:]]*image:' <<<"$manifest" \
            | grep -vE '^[0-9]+:[[:space:]]*image: softwarity/plug:e2e([[:space:]]|$)' || true)"
[ -z "$leftover" ] \
  || rewrite_failed="$rewrite_failed${rewrite_failed:+; }unrewritten image reference(s): $(tr '\n' ' ' <<<"$leftover")"

if [ -n "$rewrite_failed" ]; then
  echo "::error::deploy/plug-k8s.yaml no longer matches what k8s-serve.sh rewrites ($rewrite_failed). Unfixed, the kind cluster would have deployed the PUBLISHED agent image instead of this commit, and the nine e2e-k8s legs would have gone green without ever running the branch. Realign the sed anchors in scripts/ci/k8s-serve.sh with the manifest." >&2
  exit 1
fi

printf '%s\n' "$manifest" | kubectl apply -f -

# The update cells start from a PUBLISHED release, so this one is NOT rewritten
# to the branch image: it is pulled from the registry as 2.4.1, which is the
# whole point (the oldest agent that can retarget itself). One namespace each —
# see the manifest for why.
echo "=== deploy the per-leg previous-release agents (update cells) ==="
# The image is resolved, not pinned — see scripts/ci/previous-release.sh.
PREV_RELEASE="$(bash "$root/scripts/ci/previous-release.sh")" || exit 1
echo "previous release = $PREV_RELEASE"
export PREV_RELEASE
envsubst '$PREV_RELEASE' < "$root/e2e/k8s.prev-agents.yaml" | kubectl apply -f -

echo "=== deploy the per-leg crash-test agents + chaos (resilience, lease) ==="
kubectl apply -f "$root/e2e/k8s.res-agents.yaml"

echo "=== deploy the services ==="
kubectl create configmap rabbitmq-config \
  --from-file=rabbitmq.conf=services/rabbitmq/rabbitmq.conf \
  --from-file=definitions.json=services/rabbitmq/definitions.json
kubectl create configmap mosquitto-config \
  --from-file=mosquitto.conf=services/mosquitto/mosquitto.conf
# Only PLUG_CLUSTER_IDENT is substituted (an unrestricted envsubst would eat
# every $… in the probes' shell commands).
PLUG_CLUSTER_IDENT="${PLUG_CLUSTER_IDENT:-solo}" \
  envsubst '$PLUG_CLUSTER_IDENT' < k8s.cluster.yaml | kubectl apply -f -

echo "=== wait for every deployment ==="
# One rollout status per deployment — NOT `kubectl wait --all`, which checks
# sequentially: one stuck deployment eats the whole timeout and the rest get
# reported "timed out" unchecked (that red herring cost a run). On failure,
# dump what actually happened before dying — the runner (and the cluster's
# state) is gone right after.
failed=""
for d in $(kubectl get deploy -o name); do
  kubectl rollout status --timeout=180s "$d" || failed="$failed $d"
done
# The previous-release agents live in their own namespaces, which the loop above does not see.
for ns in plug-prev-linux plug-prev-mac plug-prev-win plug-res-linux plug-res-mac plug-res-win; do
  kubectl -n "$ns" rollout status --timeout=180s deploy/plug || failed="$failed $ns/plug"
done
kubectl get deploy,svc -o wide
if [ -n "$failed" ]; then
  echo "=== NOT READY:$failed — diagnostic dump ===" >&2
  kubectl get pods -o wide >&2
  for p in $(kubectl get pods --field-selector=status.phase!=Running -o name; \
             kubectl get pods -o json | jq -r '.items[] | select([.status.containerStatuses[]? | .ready] | all | not) | "pod/\(.metadata.name)"'); do
    echo "--- describe $p ---" >&2; kubectl describe "$p" | tail -25 >&2
    echo "--- logs $p ---" >&2; kubectl logs "$p" --tail=30 >&2 || true
  done
  exit 1
fi

echo "=== kubectl port-forward as a transport (the zero-exposed-port path) ==="
# The legs reach the agent through the NodePort; the OTHER documented way in —
# a port-forward riding the API server, gated by kubeconfig RBAC — is proven
# here on every push: the forwarded port must serve the agent's ssh contract.
kubectl port-forward svc/plug 2223:2222 >/dev/null 2>&1 &
pf=$!
ok=""
for _ in $(seq 1 15); do
  v="$(ssh -n -p 2223 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR -o BatchMode=yes -o ConnectTimeout=3 get@127.0.0.1 version 2>/dev/null || true)"
  [ -n "$v" ] && { echo "port-forward OK — agent answers through it (version: $v)"; ok=1; break; }
  sleep 2
done
kill "$pf" 2>/dev/null || true
[ -n "$ok" ] || { echo "agent did not answer through kubectl port-forward" >&2; exit 1; }

bash "$root/scripts/ci/idle-until-caller-done.sh"
