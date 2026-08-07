#!/usr/bin/env bash
# Build the e2e clients ONCE, for every leg.
#
# Measured on the Windows swarm leg: 493s of a 769s setup went into building the
# four language clients — go 278s, node 99s, java 74s, python 42s — and the three
# Windows legs each paid it in full, for artefacts that do not differ between
# them. The clients are test scaffolding, not the product; what varies from one
# leg to the next is the CLUSTER in front of them, never the client itself.
#
# What that lets us mutualise, and why:
#   · Go     — every driver in the e2e client is pure Go (paho, pq, amqp091,
#              go-redis, mongo-driver, grpc-go, gorilla/websocket). No cgo, so
#              ONE Linux runner cross-compiles all five targets. Same for the
#              echo-local and sink helpers, which nine cells rebuild each.
#   · Java   — a jar is bytecode. One build, every OS.
#   · Node   — every dependency here is pure JS (grpc-js is the JS
#              implementation, on purpose). node_modules travels as a tar,
#              because an artefact of thousands of small files uploads slower
#              than it rebuilds.
#   · Python — NOT here: psycopg2-binary and grpcio are compiled wheels, so the
#              install is per-OS. It costs 42s and its pip cache works.
#
# What this gives up, said plainly: we stop proving that the test client
# COMPILES on Windows and macOS. That is not a promise plug makes — the product
# itself is cross-built in _docker.yml and exercised natively on every leg.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
out="${1:-$root/e2e-clients}"
mkdir -p "$out"

# Exactly the four targets the CI legs run on — darwin-amd64 is deliberately
# absent, macos-latest being arm64. If that label ever flips, `gtag` finds no
# binary for its target and the leg builds its own: a runner we did not
# anticipate degrades to the old cost, never to a wrong artefact.
TARGETS="linux-amd64 linux-arm64 darwin-arm64 windows-amd64"

# -s -w drops the symbol table and DWARF from test scaffolding nobody debugs
# with a stack trace: the bundle every leg downloads shrinks by about a third.
LDFLAGS="-s -w"

say() { echo "=== $* ==="; }

say "go — the e2e client and the two helpers, cross-compiled for $TARGETS"
for t in $TARGETS; do
  goos="${t%-*}"; goarch="${t#*-}"; sfx=""
  [ "$goos" = windows ] && sfx=".exe"
  for m in clients/go:eclient echo-local:echo-local sink:sink; do
    dir="${m%%:*}"; bin="${m##*:}"
    ( cd "$root/e2e/$dir" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build -ldflags "$LDFLAGS" -o "$out/$bin-$t$sfx" . )
  done
  echo "  $t: ok"
done

say "java — one jar for every OS"
( cd "$root/e2e/clients/java" && mvn -e -B package )
cp "$root/e2e/clients/java/target/client.jar" "$out/client.jar"
echo "  client.jar: ok"

say "node — node_modules, tarred"
( cd "$root/e2e/clients/node" && npm install --omit=dev --no-audit --no-fund )
tar -czf "$out/node_modules.tar.gz" -C "$root/e2e/clients/node" node_modules
echo "  node_modules.tar.gz: ok"

# A silent partial build would send a leg into its fallback and rebuild
# everything, which is exactly the cost this script exists to remove — so count
# what came out and fail here, where the cause is visible.
want=$(( $(echo "$TARGETS" | wc -w) * 3 + 2 ))
got=$(find "$out" -type f | wc -l)
[ "$got" -eq "$want" ] || { echo "expected $want artefacts, produced $got:" >&2; ls -la "$out" >&2; exit 1; }

say "$got artefacts"
ls -la "$out"
