#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# dns honesty: a name ABSENT from the cluster must answer NXDOMAIN (plug asks
# the agent before minting), not hand out a fake IP that can only refuse the
# connect. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
do_dnshonesty() {
  echo "=== dns honesty: an absent name must NXDOMAIN ==="
  local nx
  nx="$(plug curl -sS --max-time 8 "http://absent-name-e2e:9/" 2>&1 | tr -d '\r' | tail -1)"
  if printf '%s' "$nx" | grep -qiE "could not resolve|no such host|name or service not known"; then
    echo "dns OK - absent-name-e2e answered NXDOMAIN (honest resolution failure)"
    sum "**dns honesty (absent → NXDOMAIN)** ✅"
  else
    echo "--- dns FAIL - expected a resolution error, got: ${nx:-<nothing>}"
    sum "**dns honesty (absent → NXDOMAIN)** ❌ - \`${nx:-nothing}\`"; return 1
  fi
  dns_short_form
}

# <service>.<namespace>, the way a pod names a Service of another namespace and
# a Helm chart writes it (http://opentelemetry.monitoring:4318). A pod resolves
# it through its search domains; a plugged process must too. Kubernetes only:
# Compose and Swarm have no namespaces. httpbin is in `default`
# (e2e/k8s.cluster.yaml carries no namespace).
dns_short_form() {
  if [ "$family" != k8s ]; then
    echo "service.namespace: not measured on $family (no namespaces)"
    return 0
  fi
  local code
  code="$(plug curl -sS --max-time 15 -o /dev/null -w '%{http_code}' "http://httpbin.default:8080/get" 2>/tmp/dns-short.err | tr -d '\r' | tail -1)"
  if [ "$code" = 200 ]; then
    echo "dns OK - httpbin.default (service.namespace) answered 200"
    sum "**service.namespace resolves** ✅"
  else
    echo "--- dns FAIL - httpbin.default (service.namespace) answered '${code:-nothing}': $(head -c 200 /tmp/dns-short.err 2>/dev/null)"
    sum "**service.namespace resolves** ❌ - \`${code:-nothing}\`"; return 1
  fi
}
