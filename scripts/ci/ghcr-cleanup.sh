#!/usr/bin/env bash
# ghcr-cleanup.sh <short-sha> - delete this run's test images from GHCR, and any
# a dead run left behind.
#
# The CI pushes its test images to ghcr.io/softwarity/plug (_docker.yml): the
# clusters, the legs and the publish job read them, and the publish job copies
# the tested digest to Docker Hub. Past the run they are nobody's, and kept they
# would pile up there the way the sha-* tags piled up on Docker Hub. So ci.yml
# calls this at the end of every run, green, red or cancelled.
#
# This run's: every version behind the three tags it pushed (the standalone and
# hosted manifest lists, the amd64 one the clusters pull), the platform images
# and attestations under them included, which carry no tag of their own.
# Leftovers: any version older than two days, from a run that died before its
# own cleanup. Nothing else ever lives in this package.
#
# Needs GH_TOKEN with packages: write, docker logged in to ghcr.io, jq.
set -euo pipefail

short="${1:?usage: ghcr-cleanup.sh <short-sha>}"
org=softwarity pkg=plug image=ghcr.io/softwarity/plug
api="/orgs/$org/packages/container/$pkg/versions"

# The digests this run published: each tag's own, and those under it.
mine="$(mktemp)"
for tag in "sha-$short" "sha-$short-hosted" "sha-$short-amd64"; do
  raw="$(docker buildx imagetools inspect --raw "$image:$tag" 2>/dev/null)" || continue
  docker buildx imagetools inspect "$image:$tag" --format '{{.Manifest.Digest}}' >> "$mine"
  printf '%s' "$raw" | jq -r '.manifests[]?.digest' >> "$mine"
done

cutoff="$(date -u -d '2 days ago' +%Y-%m-%dT%H:%M:%SZ)"
versions="$(gh api --paginate "$api?per_page=100" --jq '.[] | "\(.id) \(.name) \(.updated_at)"')" || {
  echo "::error::cannot list the versions of $image: the job's token needs packages: write, and the package must grant this repository access"
  exit 1
}

deleted=0 failed=0
while read -r id name updated; do
  [ -n "$id" ] || continue
  if grep -qxF "$name" "$mine" || [[ "$updated" < "$cutoff" ]]; then
    if gh api -X DELETE "$api/$id" >/dev/null; then
      deleted=$((deleted + 1))
    else
      failed=$((failed + 1))
    fi
  fi
done <<< "$versions"

echo "deleted $deleted version(s) of $image (this run's, and leftovers older than $cutoff)"
if [ "$failed" -gt 0 ]; then
  echo "::error::$failed version(s) of $image could not be deleted: the repository needs the Admin role on the package (package settings, Manage Actions access)"
  exit 1
fi
