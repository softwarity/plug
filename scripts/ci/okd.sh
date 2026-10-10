#!/usr/bin/env bash
# plug on a real OpenShift, small enough for a CI runner: MicroShift built from
# OKD, one node, in a container. What kind cannot imitate is here for real: the
# restricted-v2 SCC that ADMITS the agent's pod and runs it as a uid the
# cluster chose, never root. The same harness Meerkat runs its gateway on.
#
#   scripts/ci/okd.sh <image>
#
# <image> is the agent image under test, already published for this commit
# (sha-<commit>-amd64). It is pulled here and copied into the node's own
# storage, so what runs is what was built.
#
# The manifest is the PUBLISHED one, deploy/plug-k8s.yaml, with only its image
# swapped, as on kind. Then plug is installed from that agent and used: a
# service reached by name (bare and service.namespace), and a name served from
# the runner and reached from a pod. The volume mount is not here: MicroShift's
# storage wants an LVM volume group on the host, which a shared runner does not
# have, and its helper pod under restricted-v2 is what Meerkat's own OKD run
# covers.
#
# KEEP=1 leaves the cluster up to look at.
set -euo pipefail

IMAGE=${1:?usage: okd.sh <image>}
# Pinned: `latest` there moves with every upstream build. The one Meerkat runs.
MICROSHIFT_IMAGE=${MICROSHIFT_IMAGE:-ghcr.io/microshift-io/microshift:4.21.0_g29f429c21_4.21.0_okd_scos.ec.15}
NODE=${NODE:-plug-okd}
NS=plug-okd
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
KC=/var/lib/microshift/resources/kubeadmin/kubeconfig
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10"

passed=0
say() { printf '\n== %s\n' "$*"; }
ok() { passed=$((passed + 1)); printf '  ok    %s\n' "$*"; }
die() { printf '  FAIL  %s\n' "$*" >&2; exit 1; }
node() { docker exec -i "$NODE" env KUBECONFIG=$KC "$@"; }
oc() { node oc "$@"; }
expect() { [ "$2" = "$3" ] && ok "$1: $3" || die "$1: wanted $2, got $3"; }
until_ok() {
  local limit=$1 what=$2 t0=$SECONDS
  shift 2
  until "$@" >/dev/null 2>&1; do
    [ $((SECONDS - t0)) -lt "$limit" ] || die "$what: still not true after ${limit}s"
    sleep 5
  done
  ok "$what (${SECONDS}s in)"
}

finish() {
  local code=$?
  if [ $code -ne 0 ]; then
    say "what the cluster looked like"
    oc get pods -A -o wide 2>&1 | head -40 || true
    oc -n $NS describe pods 2>&1 | tail -60 || true
    oc -n $NS logs deploy/plug --tail=40 2>&1 || true
    [ -f /tmp/okd-serve.out ] && { say "the serving session"; tail -20 /tmp/okd-serve.out; } || true
  fi
  [ -n "${serve_pid:-}" ] && kill "$serve_pid" 2>/dev/null || true
  [ "${KEEP:-}" = 1 ] || docker rm -f "$NODE" >/dev/null 2>&1 || true
  exit $code
}
trap finish EXIT

say "MicroShift ($MICROSHIFT_IMAGE)"
docker rm -f "$NODE" >/dev/null 2>&1 || true
docker run --privileged -d --tty --name "$NODE" --hostname 127.0.0.1.nip.io "$MICROSHIFT_IMAGE" >/dev/null
until_ok 300 "the API answers" node test -f $KC
node_ready() { oc get nodes --no-headers | grep -q ' Ready'; }
until_ok 1200 "the node is Ready" node_ready

say "the image under test, copied into the cluster"
docker pull -q "$IMAGE" >/dev/null
docker save "$IMAGE" | node sh -c 'cat > /tmp/image.tar && skopeo copy -q docker-archive:/tmp/image.tar containers-storage:localhost/plug:under-test && rm /tmp/image.tar'
ok "$IMAGE is localhost/plug:under-test in the node"

say "the published manifest, deploy/plug-k8s.yaml"
manifest=$(sed -e 's|image: docker.io/softwarity/plug:latest|image: localhost/plug:under-test|' \
               -e 's|imagePullPolicy: Always|imagePullPolicy: Never|' "$ROOT/deploy/plug-k8s.yaml")
# Asserted, as on kind: a manifest that kept its own image would be pulled from
# the registry, and this would be a test of the last release.
grep -qE '^[[:space:]]*image: localhost/plug:under-test' <<<"$manifest" || die "the manifest does not run the image under test"
grep -qE '^[[:space:]]*imagePullPolicy: Never' <<<"$manifest" || die "the manifest still pulls"
# What OpenShift refuses, checked on what we publish rather than found there.
if grep -Eq '^\s*(runAsUser|runAsGroup|fsGroup):' <<<"$manifest"; then die "the manifest pins a uid or a group: the restricted SCC rejects such a pod"; fi
ok "the manifest runs the image under test and pins no uid"
oc create namespace $NS >/dev/null
echo "$manifest" | oc apply -n $NS -f - >/dev/null
oc -n $NS rollout status deploy/plug --timeout=300s >/dev/null || die "the agent did not roll out"
ok "the agent rolled out"

say "admitted by the SCC, as the uid the cluster chose"
pod=$(oc -n $NS get pod -l app=plug -o name | head -1)
expect "SCC" restricted-v2 "$(oc -n $NS get "$pod" -o jsonpath='{.metadata.annotations.openshift\.io/scc}')"
uid=$(oc -n $NS exec "$pod" -- id -u)
[ "$uid" -ge 1000000000 ] || die "the agent runs as uid $uid: the SCC did not allocate it"
ok "the agent runs as uid $uid, not root"
expect "group" 0 "$(oc -n $NS exec "$pod" -- id -g)"
oc -n $NS exec "$pod" -- test -s /var/lib/plug/state/host_key || die "no host key in /var/lib/plug/state: the state directory is not writable by the pod's uid"
ok "the agent wrote its host key"
oc -n $NS logs "$pod" | grep -q "ready (v" || die "the agent's log does not say it is ready"
ok "the agent says it is ready"

say "a service to reach, admitted the same way"
oc -n $NS apply -f - >/dev/null <<'HTTPBIN'
apiVersion: apps/v1
kind: Deployment
metadata: {name: httpbin}
spec:
  replicas: 1
  selector: {matchLabels: {app: httpbin}}
  template:
    metadata: {labels: {app: httpbin}}
    spec:
      containers:
      - name: httpbin
        image: docker.io/mccutchen/go-httpbin:v2.15.0
        ports: [{containerPort: 8080}]
        readinessProbe: {tcpSocket: {port: 8080}, periodSeconds: 2}
        securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}, runAsNonRoot: true, seccompProfile: {type: RuntimeDefault}}
---
apiVersion: v1
kind: Service
metadata: {name: httpbin}
spec:
  selector: {app: httpbin}
  ports: [{port: 8080, targetPort: 8080}]
HTTPBIN
oc -n $NS rollout status deploy/httpbin --timeout=300s >/dev/null || die "httpbin did not roll out"
ok "httpbin runs"

say "plug installed from that agent, through its NodePort"
ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$NODE")
port=32222
# shellcheck disable=SC2086 # SSH_OPTS is a list of options
reachable() { ssh -n -p $port $SSH_OPTS "get@$ip" version; }
until_ok 120 "the agent answers on $ip:$port" reachable
# shellcheck disable=SC2086
ssh -p $port $SSH_OPTS "get@$ip" install okd </dev/null | sh || die "the install from the agent failed"
PLUG="$HOME/.local/bin/plug"
[ -x "$PLUG" ] || die "plug not found after install"
grep -qx "host = $ip" "$HOME/.plug/okd.conf" || die "the install did not save the cluster as profile okd"
ok "installed, profile okd -> $ip:$port"
"$PLUG" test -p okd || die "plug test cannot reach the agent"
ok "plug test reaches the agent"

say "the cluster's services, by name"
code() { "$PLUG" -p okd -c curl -s --max-time 15 -o /dev/null -w '%{http_code}' "$1" 2>/dev/null | tr -d '\r' | tail -1; }
expect "http://httpbin:8080/get" 200 "$(code http://httpbin:8080/get)"
expect "http://httpbin.$NS:8080/get (service.namespace)" 200 "$(code "http://httpbin.$NS:8080/get")"

say "a name served from this machine, reached from a pod"
mkdir -p /tmp/okd-www && echo served-from-the-runner > /tmp/okd-www/index.html
"$PLUG" -p okd -s okd-echo:8080:18099 python3 -m http.server 18099 --directory /tmp/okd-www >/tmp/okd-serve.out 2>&1 &
serve_pid=$!
served() { grep -q "path verified" /tmp/okd-serve.out; }
until_ok 90 "the session serves okd-echo" served
oc -n $NS run probe --restart=Never --image=docker.io/curlimages/curl:8.11.1 \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"probe","image":"docker.io/curlimages/curl:8.11.1","args":["-s","--max-time","20","http://okd-echo:8080/"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' >/dev/null
probe_done() { [ "$(oc -n $NS get pod probe -o jsonpath='{.status.phase}')" = Succeeded ]; }
until_ok 180 "the probe pod finished" probe_done
expect "a pod reaching okd-echo" served-from-the-runner "$(oc -n $NS logs probe | tr -d '\r' | tail -1)"

say "$passed checks passed"
