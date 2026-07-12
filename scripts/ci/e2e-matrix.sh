#!/usr/bin/env bash
# Full protocol matrix, run NATIVELY on this runner (macOS, or Windows via Git
# Bash): build plug + the four language clients, then run each client UNDER plug
# against each cluster service BY NAME over the Tailscale mesh, and render a
# PASS/FAIL grid. Same coverage as the Linux docker matrix (e2e/matrix.sh), but
# native — which only a real macOS/Windows host can prove.
#
#   e2e-matrix.sh <cluster-tailnet-name> [port]
#
# Portable to macOS's bash 3.2: no associative arrays.
set -uo pipefail
peer="${1:?usage: e2e-matrix.sh <cluster-tailnet-name> [port]}"
port="${2:-2222}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
clients="$root/e2e/clients"
cd "$root/cli"

LANGS="go node python java"
# proto:host:port — the by-name target for each service (matches e2e/matrix.sh).
PROTOS="http:httpbin:8080 postgres:postgres:5432 redis:redis:6379 mongo:mongo:27017 amqp:rabbitmq:5672 mqtt:mosquitto:1883 grpc:grpc:50051 websocket:wsserver:8090"

# --- OS specifics: plug binary + privilege + python name ---
ext=""; sudo=""; py="python3"
case "$(uname -s)" in
  Darwin|Linux) [ "$(id -u)" = 0 ] || sudo=sudo ;;
  MINGW*|MSYS*|CYGWIN*) ext=".exe"; py="python" ;;
esac

echo "=== build plug$ext ==="
go build -o "plug$ext" .
if [ "$ext" = ".exe" ]; then
  echo "=== fetch wintun.dll ==="
  curl -sL https://www.wintun.net/builds/wintun-0.14.1.zip -o wintun.zip
  powershell -NoProfile -Command "Expand-Archive -Path wintun.zip -DestinationPath wtun -Force"
  cp wtun/wintun/bin/amd64/wintun.dll .
fi

# --- wait for the cluster over the tailnet ---
echo "=== wait for cluster $peer:$port ==="
ip=""
for _ in $(seq 1 90); do
  ip="$(tailscale ip -4 "$peer" 2>/dev/null | head -1 || true)"
  if [ -n "$ip" ] && "./plug$ext" test --host "$ip" --port "$port" >/dev/null 2>&1; then
    echo "cluster reachable at $ip:$port"; break
  fi
  ip=""; sleep 3
done
[ -n "$ip" ] || { echo "cluster $peer never became reachable" >&2; exit 1; }

# Per-cell timeout: a client with no timeout of its own must not hang the whole
# job. perl's alarm is on every runner (incl. Git Bash) and survives exec.
plug() { perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 $sudo "./plug$ext" --host "$ip" --port "$port" "$@"; }

# --- build the four language clients natively ---
echo "=== build clients ==="
build_go()     { ( cd "$clients/go" && go build -o "eclient$ext" . ); }
build_node()   { ( cd "$clients/node" && npm install --omit=dev --no-audit --no-fund ); }
# --break-system-packages: macOS runners ship a Homebrew Python that refuses a
# plain `pip install` (externally-managed-environment).
build_python() { $py -m pip install --quiet --disable-pip-version-check --user --break-system-packages -r "$clients/python/requirements.txt"; }
build_java()   { ( cd "$clients/java" && mvn -e -B package ); } # no -q: surface the goal on failure

built=""
for l in $LANGS; do
  if "build_$l" >"/tmp/build-$l.log" 2>&1; then
    built="$built $l"; echo "  $l: ok"
  else
    echo "  $l: BUILD FAILED"; tail -20 "/tmp/build-$l.log" | sed 's/^/    /'
  fi
done

# --- the client command (under plug) per language ---
cmd_go()     { echo "$clients/go/eclient$ext"; }
cmd_node()   { echo "node $clients/node/client.js"; }
cmd_python() { echo "$py $clients/python/client.py"; }
cmd_java()   { echo "java -jar $clients/java/target/client.jar"; }

# --- run the matrix ---
echo "=== matrix: each client UNDER plug → service by name ==="
results=""; fails=0
for entry in $PROTOS; do
  proto="${entry%%:*}"; target="${entry#*:}"
  for l in $LANGS; do
    case " $built " in *" $l "*) : ;; *) results="$results$l $proto SKIP
"; continue ;; esac
    # 2 attempts: the mesh datapath can blip transiently on the first hit of a service.
    r=FAIL; out=""
    for _attempt in 1 2; do
      out="$(plug $("cmd_$l") "$proto" "$target" 2>&1)"
      if printf '%s' "$out" | grep -q "E2E-OK"; then r=PASS; break; fi
      sleep 2
    done
    results="$results$l $proto $r
"
    if [ "$r" != PASS ]; then
      fails=$((fails + 1))
      echo "--- $l / $proto FAIL ---"; printf '%s\n' "$out" | tail -8 | sed 's/^/    /'
    fi
  done
done

# --- render the grid ---
lookup() { printf '%s\n' "$results" | awk -v a="$1" -v b="$2" '$1==a && $2==b {print $3}'; }
glyph()  { case "$1" in PASS) printf "✅" ;; FAIL) printf "❌" ;; SKIP) printf "·" ;; *) printf "?" ;; esac; }
protolist=""; for e in $PROTOS; do protolist="$protolist ${e%%:*}"; done

render() {
  echo "## plug mesh e2e — $(uname -s) · by name over the mesh"
  echo
  printf "| client |"; for p in $protolist; do printf " %s |" "$p"; done; echo
  # printf '%s': a format string starting with '-' is otherwise read as a flag.
  printf '%s' "|---|"; for p in $protolist; do printf '%s' "---|"; done; echo
  for l in $LANGS; do
    printf "| **%s** |" "$l"
    for p in $protolist; do printf " %s |" "$(glyph "$(lookup "$l" "$p")")"; done; echo
  done
}
render | tee -a "${GITHUB_STEP_SUMMARY:-/dev/stderr}"

echo "=== $fails failure(s) ==="
[ "$fails" = 0 ]
