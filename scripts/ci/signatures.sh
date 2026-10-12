#!/usr/bin/env bash
# signatures.sh sign|verify <image@digest|image:tag>... - the cosign signature
# of a published plug image, made and checked the one way.
#
# Keyless: the signature carries the identity of the workflow that pushed the
# image, ci.yml on main, and that identity is what is checked. There is no key
# to keep or to hand out. The same identity is on the security page of the
# docs, which is what a user runs: change one, change both.
#
# What is signed is the digest, recursively: the manifest list and each
# per-architecture image under it, since a client pulls one architecture and
# verifies what it pulled. A tag is resolved to its digest first, so the
# signature names bytes, never a name that moves.
#
# verify reads back every one of them, index and platform images: a signature
# nobody checked is how an image ships without one and nothing says so.
#
# Needs cosign, docker buildx and jq, logged in to the registry; sign needs the
# job's id-token: write.
set -euo pipefail

ISSUER=https://token.actions.githubusercontent.com
IDENTITY='^https://github\.com/softwarity/plug/\.github/workflows/ci\.yml@refs/heads/main$'

repo_of() {
  case "$1" in
    *@*) echo "${1%@*}" ;;
    *) echo "${1%:*}" ;;
  esac
}

digest_of() {
  docker buildx imagetools inspect "$1" --format '{{.Manifest.Digest}}'
}

# The platform images under an index. The attestation manifests
# (unknown/unknown) are signed with the rest by --recursive, but nobody runs
# them, so they are not what verify asks about.
children() {
  docker buildx imagetools inspect --raw "$1" |
    jq -r '.manifests[]? | select(.platform.os != "unknown") | .digest'
}

signed() {
  cosign verify --certificate-oidc-issuer "$ISSUER" --certificate-identity-regexp "$IDENTITY" "$1" >/dev/null 2>&1
}

sign() {
  local ref repo d
  for ref in "$@"; do
    repo=$(repo_of "$ref"); d=$(digest_of "$ref")
    echo "signing $ref ($d)"
    cosign sign --yes --recursive "$repo@$d"
  done
}

verify() {
  local ref repo d
  for ref in "$@"; do
    repo=$(repo_of "$ref"); d=$(digest_of "$ref")
    for x in "$d" $(children "$repo@$d"); do
      signed "$repo@$x" || { echo "::error::$ref: $repo@$x is not signed by $IDENTITY"; exit 1; }
    done
    echo "signed   $ref ($d and its platform images)"
  done
}

case "${1:-}" in
  sign | verify) cmd=$1; shift; [ $# -gt 0 ] || { echo "usage: signatures.sh sign|verify <image>..." >&2; exit 2; }; "$cmd" "$@" ;;
  *) echo "usage: signatures.sh sign|verify <image>..." >&2; exit 2 ;;
esac
