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

echo "=== install kind (latest release) ==="
sudo curl -fsSLo /usr/local/bin/kind \
  https://github.com/kubernetes-sigs/kind/releases/latest/download/kind-linux-amd64
sudo chmod +x /usr/local/bin/kind
kind version

echo "=== create the kind cluster ==="
kind create cluster --config "$root/e2e/kind-config.yaml" --wait 120s

echo "=== build + load the local service images ==="
cd "$root/e2e"
for svc in grpc prober flaky gateway; do
  docker build -q -t "plug-e2e/$svc:e2e" -f "services/$svc/Dockerfile" . >/dev/null
done
docker build -q -t plug-e2e/wsserver:e2e -f services/websocket/Dockerfile . >/dev/null
kind load docker-image softwarity/plug:e2e \
  plug-e2e/grpc:e2e plug-e2e/wsserver:e2e plug-e2e/prober:e2e \
  plug-e2e/flaky:e2e plug-e2e/gateway:e2e

echo "=== deploy the agent (the PUBLISHED manifest, branch image) ==="
sed -e 's|image: docker.io/softwarity/plug:latest|image: softwarity/plug:e2e|' \
    -e 's|imagePullPolicy: Always|imagePullPolicy: Never|' \
    "$root/deploy/plug-k8s.yaml" | kubectl apply -f -

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

echo "=== cluster up — serving for ${ttl}s (or until this run is cancelled) ==="
sleep "$ttl"
