#!/usr/bin/env bash
# The deployment files the site serves for download, checked as served: the
# copies docs/scripts/gen-version.mjs pinned into the build, not their sources.
# Run by deploy-doc.yml after the build, so a file that does not validate, or
# whose pin did not apply, is never published.
#
# What it proves is the shape: each file parses as Compose (or as Kubernetes
# YAML) and names the release it is pinned to. That they deploy an agent that
# works is the published e2e cell's part, on Compose and Swarm, and the k8s
# family's, whose agent is deploy/plug-k8s.yaml.
#
#   check-served-deploy-files.sh <assets dir>
set -euo pipefail
dir="${1:?usage: check-served-deploy-files.sh <assets dir>}"
version="$(git describe --tags --abbrev=0 | sed 's/^v//')"
pin="docker.io/softwarity/plug:$version"
bad=""
fail() { bad="$bad"$'\n'"  $1"; }

for f in plug-service.yml plug-stack.yml plug-k8s.yaml; do
  [ -s "$dir/$f" ] || { fail "$f is not in the build"; continue; }
  # Every image line, commented examples included, is the release: a reader
  # copies the comment as readily as the line under it.
  latest="$(grep -n 'softwarity/plug:latest' "$dir/$f" || true)"
  [ -z "$latest" ] || fail "$f still names :latest (line $(tr '\n' ' ' <<<"$latest"))"
  grep -qF "image: $pin" "$dir/$f" || fail "$f does not name $pin"
done

if [ -s "$dir/plug-k8s.yaml" ]; then
  ! grep -q 'imagePullPolicy: Always' "$dir/plug-k8s.yaml" \
    || fail "plug-k8s.yaml keeps imagePullPolicy: Always, meant for :latest"
fi

# Compose's own model, the one docker compose and docker stack deploy read.
for f in plug-service.yml plug-stack.yml; do
  [ -s "$dir/$f" ] || continue
  out="$(docker compose -f "$dir/$f" config -q 2>&1)" || fail "$f is not a valid Compose file: $out"
done

if [ -n "$bad" ]; then
  echo "::error::the deployment files the site would serve do not check out:$bad" >&2
  exit 1
fi
echo "the served deployment files name $pin and validate"
