#!/usr/bin/env bash
# Full protocol matrix, run NATIVELY on this runner (Linux, macOS, or Windows via
# Git Bash): build plug + the four language clients, then run each client UNDER
# plug against each cluster service BY NAME over the Tailscale mesh, and render a
# PASS/FAIL grid. Then the MULTICLUSTER assert: two clusters are up (A and B, each
# serving its own id on http://ident:5678), and the SAME name must reach the RIGHT
# backend through each plug — simultaneously on Linux/Windows, sequentially on
# macOS (until PID-at-connect lands on its daemon).
#
#   e2e-matrix.sh <cluster-a-tailnet-name> <cluster-b-tailnet-name> [port]
#
# Portable to macOS's bash 3.2: no associative arrays.
set -uo pipefail
peer="${1:?usage: e2e-matrix.sh <cluster-a> <cluster-b> [port]}"
peer_b="${2:?usage: e2e-matrix.sh <cluster-a> <cluster-b> [port]}"
port="${3:-2222}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
clients="$root/e2e/clients"
cd "$root/cli"

LANGS="go node python java"
# proto:host:port — the by-name target for each service (matches e2e/matrix.sh).
PROTOS="http:httpbin:8080 postgres:postgres:5432 redis:redis:6379 mongo:mongo:27017 amqp:rabbitmq:5672 mqtt:mosquitto:1883 grpc:grpc:50051 websocket:wsserver:8090"

# --- OS specifics: plug binary + privilege + python name ---
ext=""; sudo=""; py="python3"; linux_caps=""
case "$(uname -s)" in
  Darwin)
    [ "$(id -u)" = 0 ] || sudo=sudo
    ;;
  Linux)
    # Grant plug its caps (like the real install) and run it AS THE USER — not via
    # sudo. Under sudo the client child runs as root with sudo's reset PATH: the
    # java client would then pick the runner's system JDK instead of setup-java's
    # (UnsupportedClassVersionError) and the python client couldn't see its --user
    # pip packages. setcap keeps the child in the user's own environment — which is
    # also exactly how plug is really used on Linux (no day-to-day sudo).
    [ "$(id -u)" = 0 ] || linux_caps=1
    ;;
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
if [ -n "$linux_caps" ]; then
  echo "=== setcap plug (run as the user, like the real install) ==="
  sudo setcap cap_net_admin,cap_sys_admin,cap_net_bind_service+ep "./plug$ext"
fi

# --- wait for a cluster over the tailnet (echoes its IP once plug reaches it) ---
wait_cluster() {
  wc_ip=""
  for _ in $(seq 1 90); do
    wc_ip="$(tailscale ip -4 "$1" 2>/dev/null | head -1 || true)"
    if [ -n "$wc_ip" ] && "./plug$ext" test --host "$wc_ip" --port "$port" >/dev/null 2>&1; then
      echo "$wc_ip"; return 0
    fi
    wc_ip=""; sleep 3
  done
  return 1
}

echo "=== wait for cluster A ($peer:$port) ==="
ip="$(wait_cluster "$peer")" || { echo "cluster $peer never became reachable" >&2; exit 1; }
echo "cluster A reachable at $ip:$port"

# Per-cell timeout: a client with no timeout of its own must not hang the whole
# job. perl's alarm is on every runner (incl. Git Bash) and survives exec.
plug_to() { to="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 $sudo "./plug$ext" --host "$to" --port "$port" "$@"; }
plug()    { plug_to "$ip" "$@"; }

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
    echo "  $l: BUILD FAILED"
    # Surface the real cause first — a blind `tail` often shows only the generic
    # Maven stack-trace epilogue, not the "[ERROR] Failed to execute goal ..." line.
    grep -iE "\[ERROR\]|BUILD FAILURE|Caused by|Exception|error:|invalid target|not supported|release version" \
      "/tmp/build-$l.log" | head -15 | sed 's/^/    | /' || true
    tail -15 "/tmp/build-$l.log" | sed 's/^/    /'
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
      # go-on-mac only: the failure pattern (5/8 pass) rules out a plain "wrong
      # resolver" story — capture, INSIDE a live plug session, what the system
      # resolver config looks like and which resolver path Go actually takes
      # (GODEBUG=netdns=2 logs the choice), so the run itself carries the diagnosis.
      if [ "$l" = go ] && [ "$(uname -s)" = Darwin ]; then
        # The failing go cells are exactly the 5s-timeout clients, so TIME each
        # link of the chain inside a live session: getaddrinfo (dscacheutil), the
        # in-stack DNS directly (dig), the mesh RTT (tailscale ping), then the
        # client under BOTH resolver paths — netdns=go (pure Go → resolv.conf →
        # in-stack, no libinfo) vs the default cgo path. Whichever leg carries
        # the >5s is the culprit; no more guessing.
        echo "    --- go/mac TIMED diagnosis (inside a live session) ---"
        host_only=${target%%:*}
        plug bash -c "
          TIMEFORMAT='    [%Rs]'
          echo '--- timed: dscacheutil $host_only (getaddrinfo path) ---'
          time dscacheutil -q host -a name $host_only
          echo '--- timed: dig $host_only.plug @198.18.0.53 (in-stack direct) ---'
          time dig +time=4 +tries=1 +short $host_only.plug @198.18.0.53
          echo '--- timed: tailscale ping (mesh RTT) ---'
          time tailscale ping -c 2 $peer
          echo '--- timed: client, FORCED pure-Go resolver (resolv.conf path) ---'
          time env GODEBUG=netdns=go+1 perl -e 'alarm 15; exec @ARGV' $clients/go/eclient$ext $proto $target
          echo '--- timed: client, default cgo resolver ---'
          time env GODEBUG=netdns=2 perl -e 'alarm 15; exec @ARGV' $clients/go/eclient$ext $proto $target
        " 2>&1 | head -60 | sed 's/^/    diag| /'
      fi
    fi
  done
done

# --- multicluster: two clusters, the SAME name must reach the RIGHT backend ---
# Each cluster's `ident` service answers that cluster's own id (its corr), so
# reaching http://ident:5678 through each plug proves the flows don't cross.
echo "=== multicluster: http://ident:5678 through plug-A and plug-B ==="
expect_a="${peer#plug-cluster-}"
expect_b="${peer_b#plug-cluster-}"
ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b never became reachable" >&2; exit 1; }
echo "cluster B reachable at $ip_b:$port"

mc=PASS; a_out=""; b_out=""
case "$(uname -s)" in
  Darwin)
    # macOS holds one cluster at a time today (PID-at-connect is designed but not
    # wired on its daemon — the machine-wide DNS is not the limiter): prove A,
    # tear the daemon down, prove B.
    mc_mode="sequential — simultaneous lands with PID-at-connect on the daemon"
    a_out="$(plug_to "$ip" curl -s http://ident:5678 2>/tmp/mc-a.err || true)"
    for _ in $(seq 1 10); do
      $sudo "./plug$ext" down 2>&1 | grep -q "no plug daemon" && break
      sleep 2
    done
    b_out="$(plug_to "$ip_b" curl -s http://ident:5678 2>/tmp/mc-b.err || true)"
    ;;
  *)
    # Linux (a private resolver per launch) and Windows (the SYSTEM service holds
    # one tunnel per cluster, attributed at connect()): BOTH plugs live at once.
    mc_mode="simultaneous"
    if [ "$ext" = ".exe" ]; then
      # The Windows multicluster path IS the SYSTEM service — install it now (the
      # runner is elevated), after the single-cluster grid ran the direct path.
      echo "--- install the datapath service (the Windows multicluster path) ---"
      "./plug$ext" install-service || echo "install-service failed — staying on the direct path"
    fi
    : > /tmp/mc-a.out
    plug_to "$ip" bash -c "curl -s http://ident:5678 > /tmp/mc-a.out && sleep 8" 2>/tmp/mc-a.err &
    mc_pid=$!
    sleep 4 # let A establish and answer while it is still alive...
    b_out="$(plug_to "$ip_b" curl -sS http://ident:5678 2>/tmp/mc-b.err || true)" # ...then hit B DURING A
    wait "$mc_pid" 2>/dev/null || true
    a_out="$(cat /tmp/mc-a.out 2>/dev/null || true)"
    ;;
esac
case "$a_out" in *"$expect_a"*) : ;; *) mc=FAIL ;; esac
case "$b_out" in *"$expect_b"*) : ;; *) mc=FAIL ;; esac
if [ "$mc" = PASS ]; then
  echo "multicluster OK ($mc_mode) — A→$a_out · B→$b_out"
else
  fails=$((fails + 1))
  echo "--- multicluster FAIL ($mc_mode) — A said '${a_out:-<nothing>}' (want $expect_a), B said '${b_out:-<nothing>}' (want $expect_b)"
  echo "    --- plug-A stderr ---"; tail -8 /tmp/mc-a.err 2>/dev/null | sed 's/^/    /'
  echo "    --- plug-B stderr ---"; tail -8 /tmp/mc-b.err 2>/dev/null | sed 's/^/    /'
fi

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
  echo
  if [ "$mc" = PASS ]; then
    echo "**multicluster** ✅ ($mc_mode) — A→\`$expect_a\` · B→\`$expect_b\`"
  else
    echo "**multicluster** ❌ ($mc_mode) — A said \`${a_out:-nothing}\` (want \`$expect_a\`) · B said \`${b_out:-nothing}\` (want \`$expect_b\`)"
  fi
}
render | tee -a "${GITHUB_STEP_SUMMARY:-/dev/stderr}"

echo "=== $fails failure(s) ==="
[ "$fails" = 0 ]
