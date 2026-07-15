#!/usr/bin/env bash
# Full protocol matrix, run NATIVELY on this runner (Linux, macOS, or Windows via
# Git Bash) — starting with the REAL user flow: plug is INSTALLED FROM THE
# CLUSTER (`install | sh` / `install-windows | bash -s --`), which grants its
# privilege the real way (setcap / setuid helper / SYSTEM service) — no sudo
# anywhere in this harness after that. Then every language client runs UNDER
# that installed plug against each cluster service BY NAME over the Tailscale
# mesh, and the grid is rendered. Finally the MULTICLUSTER assert: two clusters
# are up (A and B, each serving its own id on http://ident:5678), and the SAME
# name must reach the RIGHT backend through each plug — SIMULTANEOUSLY, on all
# three OSes.
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
cd "$root"

LANGS="go node python java"
# proto:host:port — the by-name target for each service (matches e2e/matrix.sh).
PROTOS="http:httpbin:8080 postgres:postgres:5432 redis:redis:6379 mongo:mongo:27017 amqp:rabbitmq:5672 mqtt:mosquitto:1883 grpc:grpc:50051 websocket:wsserver:8090"

# --- OS specifics ---
ext=""; py="python3"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) ext=".exe"; py="python" ;;
esac
SSH_OPTS="-p $port -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes"

# --- wait for a cluster over the tailnet (echoes its IP once its agent answers) ---
# 140×3s ≈ 7min: the cluster run now builds the agent image in its own job and
# ships it to the serve job as an artifact before anything joins the mesh.
wait_cluster() {
  wc_ip=""
  for _ in $(seq 1 140); do
    wc_ip="$(tailscale ip -4 "$1" 2>/dev/null | head -1 || true)"
    if [ -n "$wc_ip" ] && ssh -n $SSH_OPTS "get@$wc_ip" version >/dev/null 2>&1; then
      echo "$wc_ip"; return 0
    fi
    wc_ip=""; sleep 3
  done
  return 1
}

echo "=== wait for cluster A ($peer:$port) ==="
ip="$(wait_cluster "$peer")" || { echo "cluster $peer never became reachable" >&2; exit 1; }
echo "cluster A reachable at $ip:$port"

# --- install plug FROM the cluster: the exact one-liner a user runs ---
# The agent (built from THIS branch) serves the installer and the binaries; the
# installer grants the privilege (sudo setcap on Linux, sudo setuid helper on
# macOS, SCM service on the elevated Windows runner). Everything after this line
# runs plug exactly as a user does — no sudo.
echo "=== install plug from the cluster (real user flow) ==="
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    ssh -n $SSH_OPTS "get@$ip" install-windows | bash -s -- "$ip" "$port" || { echo "windows install failed" >&2; exit 1; }
    PLUG="$(cygpath "$LOCALAPPDATA")/Programs/plug/plug.exe"
    ;;
  *)
    # No -n: the unix installer reads the cluster host off this live ssh command.
    ssh $SSH_OPTS "get@$ip" install </dev/null | sh || { echo "install failed" >&2; exit 1; }
    PLUG=""
    for c in "$(command -v plug 2>/dev/null || true)" "$HOME/.local/bin/plug" /usr/local/bin/plug; do
      [ -n "$c" ] && [ -x "$c" ] && PLUG="$c" && break
    done
    ;;
esac
[ -n "$PLUG" ] && [ -x "$PLUG" ] || { echo "plug not found after install" >&2; exit 1; }
echo "installed: $PLUG"
"$PLUG" test --host "$ip" --port "$port" || { echo "installed plug cannot reach cluster A" >&2; exit 1; }

# -s is mandatory: every `plug <cmd>` names itself in the cluster. The UPWARD
# cells serve nothing, so they publish a throwaway name. ONE name+port per OS
# leg — the three legs share cluster A's agent and a remote-forward port binds
# once on it; the helper force-replaces a same-named signpost, so reusing the
# leg's name across its sequential cells is safe. The local port is a dummy: the
# startup self-test loops the nonce back inside the session, no listener needed.
case "$(uname -s)" in
  Darwin)               leg=mac;   sport=18072 ;;
  MINGW*|MSYS*|CYGWIN*) leg=win;   sport=18073 ;;
  *)                    leg=linux; sport=18071 ;;
esac
serve="-s ${leg}_run:${sport}:9"

# --- env passthrough: the child must see the caller's environment (a user's
# `FOO=bar plug npm start` / dotenv workflow depends on env AND cwd surviving
# plug's launcher → core → shim chain untouched) ---
echo "=== env passthrough ==="
fails=0 # initialized BEFORE the first cell that increments it (set -u)
env_res=FAIL
ev="$(PLUG_E2E_CANARY=canary-42 perl -e 'alarm 45; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" $serve \
  bash -c 'echo "$PLUG_E2E_CANARY"' 2>/dev/null | tr -d '\r' | tail -1)"
if [ "$ev" = "canary-42" ]; then
  env_res=PASS; echo "env OK — the child sees the caller's variables"
else
  fails=$((fails + 1)); echo "--- env FAIL — child saw '${ev:-<nothing>}' (want canary-42)"
fi

# Per-cell timeout: a client with no timeout of its own must not hang the whole
# job. perl's alarm is on every runner (incl. Git Bash) and survives exec.
plug_to() { to="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$to" --port "$port" $serve "$@"; }
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
results=""
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

# BOTH plugs live at once, on every OS: Linux gives each launch a private
# resolver; Windows' SYSTEM service and macOS' global daemon each hold one
# tunnel per cluster and attribute every flow by PID at connect — the same
# assert on all three, every run.
mc_mode="simultaneous"
mc=PASS; a_out=""; b_out=""
: > /tmp/mc-a.out
plug_to "$ip" bash -c "curl -s http://ident:5678 > /tmp/mc-a.out && sleep 8" 2>/tmp/mc-a.err &
mc_pid=$!
sleep 4 # let A establish and answer while it is still alive...
b_out="$(plug_to "$ip_b" curl -sS http://ident:5678 2>/tmp/mc-b.err || true)" # ...then hit B DURING A
wait "$mc_pid" 2>/dev/null || true
a_out="$(cat /tmp/mc-a.out 2>/dev/null || true)"
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

# --- outage recovery: a service that is DOWN then COMES BACK must become
# reachable within the SAME live session (the user report: an app keeps running
# while its dependency restarts, and plug must pick it back up). Each OS leg has
# ITS OWN flaky instance — the cluster is shared, and the first leg's /up would
# otherwise break the "starts down" premise for the others.
case "$(uname -s)" in
  Darwin) flaky=flaky-mac ;;
  MINGW*|MSYS*|CYGWIN*) flaky=flaky-win ;;
  *) flaky=flaky-linux ;;
esac
echo "=== outage recovery: $flaky down → up inside one session ==="
outage=FAIL
ol="$(plug_to "$ip" bash -c '
  f="$0"
  curl -s --max-time 6 "http://$f:8099/" >/dev/null 2>&1 && { echo "flaky answered while it should be down"; exit 1; }
  curl -s --max-time 10 "http://$f:8098/up" >/dev/null || { echo "control endpoint unreachable"; exit 1; }
  sleep 2
  for _ in 1 2 3 4 5; do
    out=$(curl -s --max-time 6 "http://$f:8099/" 2>/dev/null) && [ "$out" = "flaky-ok" ] && { echo recovered; exit 0; }
    sleep 2
  done
  echo "never recovered"; exit 1
' "$flaky" 2>/tmp/outage.err | tr -d '\r' | tail -1)"
if [ "$ol" = "recovered" ]; then
  outage=PASS; echo "outage OK — the service came back and the same session reached it"
else
  fails=$((fails + 1))
  echo "--- outage FAIL — $ol"; tail -8 /tmp/outage.err 2>/dev/null | sed 's/^/    /'
fi

# --- expose (the reverse direction): serve a runner-local port under a cluster
# name (plug -s) and have a plain cluster workload (prober) fetch it — proving
# cluster DNS name → agent alias → sshd remote-forward → this session's tunnel →
# the runner's local service. ONE NAME+PORT PER OS LEG: the legs share the
# agent, and a port can only be bound once on it.
case "$(uname -s)" in
  Darwin) exname=exposed-mac; exposeport=18082 ;;
  MINGW*|MSYS*|CYGWIN*) exname=exposed-win; exposeport=18083 ;;
  *) exname=exposed-linux; exposeport=18081 ;;
esac
echo "=== expose: $exname:$exposeport → this runner's :18086 ==="
expose=FAIL
if ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
  "$PLUG" --host "$ip" --port "$port" -s "$exname:$exposeport:18086" \
    "$root/echo-local$ext" -addr 127.0.0.1:18086 -text "expose-ok-$exname" >/tmp/expose.out 2>&1 &
  expose_pid=$!
  sleep 8 # arm + end-to-end verify (the session logs "path verified" into expose.out)
  for _ in 1 2 3; do
    eo="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$exname:$exposeport/" 2>/tmp/expose-probe.err | tr -d '\r' | tail -1)"
    [ "$eo" = "expose-ok-$exname" ] && break
    sleep 3
  done
  if [ "$eo" = "expose-ok-$exname" ]; then
    expose=PASS; echo "expose OK — a cluster workload reached this runner's local service by name"
  else
    fails=$((fails + 1))
    echo "--- expose FAIL — prober said '${eo:-nothing}' (want expose-ok-$exname)"
    echo "    --- expose session output ---"; tail -12 /tmp/expose.out 2>/dev/null | sed 's/^/    /'
    tail -6 /tmp/expose-probe.err 2>/dev/null | sed 's/^/    /'
  fi
  kill $expose_pid 2>/dev/null; wait $expose_pid 2>/dev/null
else
  fails=$((fails + 1)); echo "--- expose FAIL — echo-local did not build"
fi

# --- gateway callback (reverse direction, driven from OUTSIDE the cluster):
# serve a local sink under a cluster name, then have an EXTERNAL caller POST to
# the cluster's PUBLISHED gateway. The gateway calls the name INSIDE the cluster,
# which lands on our sink, which answers "<path> <id>" back up through the
# gateway to us. The POST goes STRAIGHT to the published gateway port (NOT
# through plug), so this proves the downward path + the gateway, not the upward
# tunnel. Two calls on the SAME session: one at the root ("/"), one at a deep
# path — proving both the id AND the full request path travel the tunnel intact.
# Per-OS name so the shared cluster's legs don't collide.
case "$(uname -s)" in
  Darwin) gwname=gwsink-mac; gwcport=18092 ;;
  MINGW*|MSYS*|CYGWIN*) gwname=gwsink-win; gwcport=18093 ;;
  *) gwname=gwsink-linux; gwcport=18091 ;;
esac
gwlocal=18096
echo "=== gateway callback: external POST → gateway → $gwname → our sink :$gwlocal ==="
gw=FAIL       # root call: {service,port,id}
gwpath=FAIL   # deep-path call: {service,port,path,id}
# gw_call <expected> <json-body> — POST to the published gateway, retry a few
# times (the dynamic name needs a beat to exist), echo PASS/FAIL.
gw_call() {
  gc_want="$1"; gc_body="$2"; gc_out=""
  for _ in 1 2 3; do
    gc_out="$(curl -s --max-time 10 -X POST "http://$ip:18090/call" \
      -H 'content-type: application/json' -d "$gc_body" 2>>/tmp/gw-post.err | tr -d '\r' | tail -1)"
    [ "$gc_out" = "$gc_want" ] && { echo PASS; return; }
    sleep 3
  done
  echo "FAIL|$gc_out"
}
if ( cd "$root/e2e/sink" && go build -o "$root/sink$ext" . ); then
  "$PLUG" --host "$ip" --port "$port" -s "$gwname:$gwcport:$gwlocal" \
    "$root/sink$ext" -addr "127.0.0.1:$gwlocal" >/tmp/gw.out 2>&1 &
  gw_pid=$!
  sleep 8 # arm + provision the name + verify
  # 1) root call — sink answers "/ <id>"
  gwnonce="cb-$gwname-$RANDOM"
  r="$(gw_call "/ $gwnonce" "{\"service\":\"$gwname\",\"port\":\"$gwcport\",\"id\":\"$gwnonce\"}")"
  if [ "$r" = PASS ]; then
    gw=PASS; echo "gateway OK — external POST reached our sink at / (id round-tripped)"
  else
    fails=$((fails + 1)); echo "--- gateway FAIL — got '${r#FAIL|}' (want '/ $gwnonce')"
  fi
  # 2) deep-path call — sink answers "/hook/<n> <id>", proving the path travelled too
  gwpnonce="cbp-$gwname-$RANDOM"; gwpath_seg="hook/$gwpnonce"
  r="$(gw_call "/$gwpath_seg $gwpnonce" "{\"service\":\"$gwname\",\"port\":\"$gwcport\",\"path\":\"$gwpath_seg\",\"id\":\"$gwpnonce\"}")"
  if [ "$r" = PASS ]; then
    gwpath=PASS; echo "gateway path OK — the full path /$gwpath_seg reached our sink intact"
  else
    fails=$((fails + 1)); echo "--- gateway path FAIL — got '${r#FAIL|}' (want '/$gwpath_seg $gwpnonce')"
  fi
  [ "$gw" = PASS ] && [ "$gwpath" = PASS ] || { echo "    --- sink session ---"; tail -12 /tmp/gw.out 2>/dev/null | sed 's/^/    /'; tail -6 /tmp/gw-post.err 2>/dev/null | sed 's/^/    /'; }
  kill $gw_pid 2>/dev/null; wait $gw_pid 2>/dev/null
else
  fails=$((fails + 2)); echo "--- gateway FAIL — sink did not build"
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
  echo
  echo "**env passthrough** $(glyph "$env_res") · **outage recovery** $(glyph "$outage") · **expose (cluster→local)** $(glyph "$expose") · **gateway callback** $(glyph "$gw") · **gateway path** $(glyph "$gwpath")"
}
render | tee -a "${GITHUB_STEP_SUMMARY:-/dev/stderr}"

echo "=== $fails failure(s) ==="
[ "$fails" = 0 ]
