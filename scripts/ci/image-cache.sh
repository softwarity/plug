#!/usr/bin/env bash
# The e2e clusters' Docker Hub images, carried between runs in the GitHub
# Actions cache rather than pulled again by every cluster of every run.
#
# Six clusters a run, each on a fresh runner with an empty Docker, each pulled
# the same dozen images: about seventy pulls a run against one account's quota,
# and a day of CI ran the quota out (429 Too Many Requests, the run dead before
# its first test). The list is e2e/images.txt.
#
#   image-cache.sh fill <list> <archive>   pull the list, save it to the archive
#   image-cache.sh load <archive>          load the archive into this Docker
#   image-cache.sh runtime <list>          print the images a cluster runs (not
#                                          the build bases): what kind must load
set -euo pipefail

images() { # images <list> [runtime]
  sed -e 's/#.*//' "$1" | awk -v only="${2:-}" 'NF { if (only == "" || $2 != "build") print $1 }'
}

case "${1:-}" in
  fill)
    list="$2" archive="$3"
    mapfile -t all < <(images "$list")
    for img in "${all[@]}"; do
      # Bounded and retried, like the tested image's pull: a pull can hang.
      ok=""
      for _ in 1 2 3; do
        if timeout 300 docker pull -q "$img" >/dev/null; then ok=1; break; fi
        sleep 10
      done
      [ -n "$ok" ] || { echo "::error::could not pull $img for the image cache"; exit 1; }
    done
    docker save "${all[@]}" | zstd -q -T0 -3 -o "$archive"
    echo "image cache filled: ${#all[@]} images, $(du -h "$archive" | cut -f1)"
    ;;
  load)
    zstd -q -dc "$2" | docker load -q
    ;;
  runtime)
    images "$2" runtime
    ;;
  *)
    echo "usage: $0 fill <list> <archive> | load <archive> | runtime <list>" >&2
    exit 2
    ;;
esac
