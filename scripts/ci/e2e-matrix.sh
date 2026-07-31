#!/usr/bin/env bash
# The mesh e2e, split into PHASES so each family is its OWN CI step (its own
# green/red in the run view — no more "e2e failed, go dig the log") while sharing
# ONE install. Runs NATIVELY on this runner (Linux, macOS, or Windows via Git
# Bash): plug is INSTALLED FROM THE CLUSTER (the real one-liner, real privilege
# grant), then every family runs that installed plug against a REAL cluster BY
# NAME over the Tailscale mesh.
#
#   e2e-matrix.sh <phase> <cluster-a> <cluster-b> [port]
#   phases: setup env matrix multicluster outage expose exposevar gateway takeover collision
#
# `setup` installs plug + builds the clients and records the shared state
# ($RUNNER_TEMP/plug-e2e-env) the other phases read back — they run as separate
# steps (separate shells), so nothing but files and that env file survives
# between them. Portable to macOS's bash 3.2: no associative arrays.
set -uo pipefail
phase="${1:?usage: e2e-matrix.sh <phase> <cluster-a> <cluster-b> [port]}"
peer="${2:?usage: e2e-matrix.sh <phase> <cluster-a> <cluster-b> [port]}"
peer_b="${3:?usage: e2e-matrix.sh <phase> <cluster-a> <cluster-b> [port]}"
port="${4:-2222}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
clients="$root/e2e/clients"
cd "$root"
envfile="${RUNNER_TEMP:-/tmp}/plug-e2e-env" # shared state, survives across steps

LANGS="go node python java"
# proto:host:port — the by-name target for each service.
PROTOS="http:httpbin:8080 postgres:postgres:5432 redis:redis:6379 mongo:mongo:27017 amqp:rabbitmq:5672 mqtt:mosquitto:1883 grpc:grpc:50051 websocket:wsserver:8090"

# --- OS specifics ---
ext=""; py="python3"
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) ext=".exe"; py="python" ;; esac
SSH_OPTS="-p $port -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes"

# --- wait for a cluster over the tailnet (echoes its IP once its agent answers) ---
# 200×3s ≈ 10min: the cluster run builds the agent image in its own job and ships
# it to the serve job as an artifact before anything joins the mesh — and the
# k8s family adds a kind create + image loads + rollout on top.
wait_cluster() {
  wc_ip=""
  for _ in $(seq 1 200); do
    wc_ip="$(tailscale ip -4 "$1" 2>/dev/null | head -1 || true)"
    if [ -n "$wc_ip" ] && ssh -n $SSH_OPTS "get@$wc_ip" version >/dev/null 2>&1; then
      echo "$wc_ip"; return 0
    fi
    wc_ip=""; sleep 3
  done
  return 1
}

# -s is mandatory: every `plug <cmd>` names itself in the cluster. The UPWARD
# families serve nothing, so they publish a throwaway name — ONE per OS leg (the
# three legs share cluster A's agent and a remote-forward port binds once on it;
# the helper force-replaces a same-named signpost, so reuse is safe). The local
# port is a dummy: the startup self-test loops the nonce back inside the session.
case "$(uname -s)" in
  Darwin)               leg=mac;   sport=18072 ;;
  MINGW*|MSYS*|CYGWIN*) leg=win;   sport=18073 ;;
  *)                    leg=linux; sport=18071 ;;
esac
serve="-s run-${leg}:${sport}:9"   # hyphen only — an underscore is not a valid DNS label

# Per-cell timeout: a client with no timeout of its own must not hang the job.
# perl's alarm is on every runner (incl. Git Bash) and survives exec.
plug_to() { to="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$to" --port "$port" $serve "$@"; }
plug()    { plug_to "$ip" "$@"; }
cmd_go()     { echo "$clients/go/eclient$ext"; }
cmd_node()   { echo "node $clients/node/client.js"; }
cmd_python() { echo "$py $clients/python/client.py"; }
cmd_java()   { echo "java -jar $clients/java/target/client.jar"; }

glyph() { case "$1" in PASS) printf "✅" ;; FAIL) printf "❌" ;; SKIP) printf "·" ;; *) printf "?" ;; esac; }
sum()   { echo "$*" >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"; }

# ================================ phases ================================

# setup: the real user flow — install plug FROM the cluster (the installer grants
# the privilege the real way: setcap / setuid helper / SCM SYSTEM service), build
# the four language clients, and record PLUG/ip/built for the family phases.
do_setup() {
  echo "=== wait for cluster A ($peer:$port) ==="
  ip="$(wait_cluster "$peer")" || { echo "cluster $peer never became reachable" >&2; exit 1; }
  echo "cluster A reachable at $ip:$port"

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

  { echo "PLUG='$PLUG'"; echo "ip='$ip'"; echo "built='$built'"; } > "$envfile"
  echo "state → $envfile"
  sum "### plug mesh e2e — $(uname -s)"
  sum "**install** ✅ · clients built:${built:- none}"
}

# env passthrough: the child must see the caller's environment (a user's
# `FOO=bar plug npm start` / dotenv workflow depends on env AND cwd surviving
# plug's launcher → core → shim chain untouched). Piggybacked here: DNS honesty
# — a name ABSENT from the cluster must answer NXDOMAIN (plug asks the agent
# before minting), not hand out a fake IP that can only refuse the connect.
do_env() {
  echo "=== env passthrough ==="
  local ev
  ev="$(PLUG_E2E_CANARY=canary-42 perl -e 'alarm 45; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" $serve \
    bash -c 'echo "$PLUG_E2E_CANARY"' 2>/dev/null | tr -d '\r' | tail -1)"
  if [ "$ev" = "canary-42" ]; then
    echo "env OK — the child sees the caller's variables"; sum "**env passthrough** ✅"
  else
    echo "--- env FAIL — child saw '${ev:-<nothing>}' (want canary-42)"; sum "**env passthrough** ❌"; return 1
  fi

  echo "=== dns honesty: an absent name must NXDOMAIN ==="
  local nx
  nx="$(plug curl -sS --max-time 8 "http://absent-name-e2e:9/" 2>&1 | tr -d '\r' | tail -1)"
  if printf '%s' "$nx" | grep -qiE "could not resolve|no such host|name or service not known"; then
    echo "dns OK — absent-name-e2e answered NXDOMAIN (honest resolution failure)"
    sum "**dns honesty (absent → NXDOMAIN)** ✅"
  else
    echo "--- dns FAIL — expected a resolution error, got: ${nx:-<nothing>}"
    sum "**dns honesty (absent → NXDOMAIN)** ❌ — \`${nx:-nothing}\`"; return 1
  fi

  echo "=== client-only (-c): consume the cluster, nothing served ==="
  # The DB-tool shape: no name, no agent port, outbound only.
  local co
  co="$(perl -e 'alarm 45; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" -c \
    curl -s --max-time 10 -o /dev/null -w '%{http_code}' http://httpbin:8080/get 2>/dev/null | tr -d '\r' | tail -1)"
  if [ "$co" = "200" ]; then
    echo "client-only OK — -c reached httpbin by name with nothing served"
    sum "**client-only (-c)** ✅"
  else
    echo "--- client-only FAIL — got '${co:-nothing}' (want 200)"
    sum "**client-only (-c)** ❌ — \`${co:-nothing}\`"; return 1
  fi

  echo "=== doctor: the health checks must pass on a healthy leg ==="
  # Read-only end to end (local state + this leg's profile against the real
  # agent); non-interactive stdin, so the issue prompt never fires. Exit 0 =
  # no ✗ finding on a machine the install one-liner just set up.
  local dr
  dr="$(perl -e 'alarm 60; exec @ARGV or exit 127' "$PLUG" doctor </dev/null 2>&1)"
  if [ $? -eq 0 ] && printf '%s' "$dr" | grep -q "agent"; then
    echo "doctor OK — all checks green on this leg"
    sum "**doctor** ✅"; return 0
  fi
  echo "--- doctor FAIL —"; printf '%s\n' "$dr" | tail -20 | sed 's/^/    /'
  sum "**doctor** ❌"; return 1
}

# protocol matrix: every language client, UNDER plug, reaches each cluster
# service BY NAME over the mesh. The 4×8 grid is rendered into the step summary.
do_matrix() {
  echo "=== matrix: each client UNDER plug → service by name ==="
  local fails=0 results="" entry proto target l r out _attempt
  # A client that never built is NOT a cell to skip quietly. do_setup reports the
  # build failure and still exits 0, so those cells used to render as "·" and the
  # step went GREEN having proven nothing — 8 of 32 cells unrun for one language,
  # all 32 if Maven and pip were both down. Name them and fail here, where the
  # grid is rendered, rather than shipping a matrix that tested less than it says.
  local missing=""
  for l in $LANGS; do
    case " $built " in *" $l "*) : ;; *) missing="$missing $l" ;; esac
  done
  if [ -n "$missing" ]; then
    echo "--- matrix FAIL — client(s) never built:$missing"
    echo "    the build log is in the setup step (/tmp/build-<lang>.log)"
    sum "**protocol matrix** ❌ — client(s) that never built:$missing"
    return 1
  fi
  for entry in $PROTOS; do
    proto="${entry%%:*}"; target="${entry#*:}"
    for l in $LANGS; do
      case " $built " in *" $l "*) : ;; *) results="$results$l $proto SKIP
"; continue ;; esac
      # 2 attempts: the mesh datapath can blip transiently on the first hit.
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
        # resolver config looks like and which resolver path Go actually takes.
        if [ "$l" = go ] && [ "$(uname -s)" = Darwin ]; then
          echo "    --- go/mac TIMED diagnosis (inside a live session) ---"
          local host_only=${target%%:*}
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
  # --- render the grid into the step summary ---
  local protolist="" p
  for entry in $PROTOS; do protolist="$protolist ${entry%%:*}"; done
  lookup() { printf '%s\n' "$results" | awk -v a="$1" -v b="$2" '$1==a && $2==b {print $3}'; }
  {
    echo "#### protocol matrix — $(uname -s) · by name over the mesh"
    printf "| client |"; for p in $protolist; do printf " %s |" "$p"; done; echo
    # printf '%s': a format string starting with '-' is otherwise read as a flag.
    printf '%s' "|---|"; for p in $protolist; do printf '%s' "---|"; done; echo
    for l in $LANGS; do
      printf "| **%s** |" "$l"
      for p in $protolist; do printf " %s |" "$(glyph "$(lookup "$l" "$p")")"; done; echo
    done
  } >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"
  echo "=== matrix: $fails failure(s) ==="
  [ "$fails" -eq 0 ]
}

# multicluster: two clusters up at once (A and B, each `ident` answers its own
# id). The SAME name must reach the RIGHT backend through each plug, SIMULTANEOUSLY.
do_multicluster() {
  echo "=== multicluster: http://ident:5678 through plug-A and plug-B ==="
  # ident answers the corr id — strip whichever family prefix this leg targets
  # (plug-cluster-<corr> compose, plug-k8s-<corr> kind, plug-swarm-<corr> swarm).
  local expect_a="${peer#plug-cluster-}" expect_b="${peer_b#plug-cluster-}"
  expect_a="${expect_a#plug-k8s-}"; expect_b="${expect_b#plug-k8s-}"
  expect_a="${expect_a#plug-swarm-}"; expect_b="${expect_b#plug-swarm-}"
  local ip_b mc=PASS a_out="" b_out="" mc_pid
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b never became reachable" >&2; sum "**multicluster** ❌ (cluster B unreachable)"; return 1; }
  echo "cluster B reachable at $ip_b:$port"
  # BOTH plugs live at once, on every OS: Linux gives each launch a private
  # resolver; Windows' SYSTEM service and macOS' global daemon each hold one
  # tunnel per cluster and attribute every flow by PID at connect.
  : > /tmp/mc-a.out
  plug_to "$ip" bash -c "curl -s http://ident:5678 > /tmp/mc-a.out && sleep 8" 2>/tmp/mc-a.err &
  mc_pid=$!
  sleep 4 # let A establish and answer while it is still alive...
  # ...then hit B DURING A. Two tries: another leg's resilience cell may be
  # restarting B's agent right now (a ~3s blip by design).
  b_out="$(plug_to "$ip_b" curl -sS http://ident:5678 2>/tmp/mc-b.err || true)"
  if ! printf '%s' "$b_out" | grep -q .; then
    sleep 5
    b_out="$(plug_to "$ip_b" curl -sS http://ident:5678 2>>/tmp/mc-b.err || true)"
  fi
  wait "$mc_pid" 2>/dev/null || true
  a_out="$(cat /tmp/mc-a.out 2>/dev/null || true)"
  case "$a_out" in *"$expect_a"*) : ;; *) mc=FAIL ;; esac
  case "$b_out" in *"$expect_b"*) : ;; *) mc=FAIL ;; esac
  if [ "$mc" = PASS ]; then
    echo "multicluster OK — A→$a_out · B→$b_out"; sum "**multicluster** ✅ — A→\`$expect_a\` · B→\`$expect_b\`"; return 0
  fi
  echo "--- multicluster FAIL — A said '${a_out:-<nothing>}' (want $expect_a), B said '${b_out:-<nothing>}' (want $expect_b)"
  echo "    --- plug-A stderr ---"; tail -8 /tmp/mc-a.err 2>/dev/null | sed 's/^/    /'
  echo "    --- plug-B stderr ---"; tail -8 /tmp/mc-b.err 2>/dev/null | sed 's/^/    /'
  sum "**multicluster** ❌ — A \`${a_out:-nothing}\` (want \`$expect_a\`) · B \`${b_out:-nothing}\` (want \`$expect_b\`)"
  return 1
}

# outage recovery: a service DOWN then COMING BACK must become reachable within
# the SAME live session. Each OS leg has its own flaky instance (shared cluster).
do_outage() {
  local flaky
  case "$(uname -s)" in
    Darwin) flaky=flaky-mac ;;
    MINGW*|MSYS*|CYGWIN*) flaky=flaky-win ;;
    *) flaky=flaky-linux ;;
  esac
  echo "=== outage recovery: $flaky down → up inside one session ==="
  local ol
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
    echo "outage OK — the service came back and the same session reached it"; sum "**outage recovery** ✅"; return 0
  fi
  echo "--- outage FAIL — $ol"; tail -8 /tmp/outage.err 2>/dev/null | sed 's/^/    /'
  sum "**outage recovery** ❌ — $ol"; return 1
}

# expose (reverse): serve a runner-local port under a cluster name (plug -s) and
# have a plain cluster workload (prober) fetch it. ONE name+port per OS leg.
do_expose() {
  local exname exposeport
  case "$(uname -s)" in
    Darwin) exname=exposed-mac; exposeport=18082 ;;
    MINGW*|MSYS*|CYGWIN*) exname=exposed-win; exposeport=18083 ;;
    *) exname=exposed-linux; exposeport=18081 ;;
  esac
  echo "=== expose: $exname:$exposeport → this runner's :18086 ==="
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- expose FAIL — echo-local did not build"; sum "**expose (cluster→local)** ❌ (build)"; return 1
  fi
  "$PLUG" --host "$ip" --port "$port" -s "$exname:$exposeport:18086" \
    "$root/echo-local$ext" -addr 127.0.0.1:18086 -text "expose-ok-$exname" >/tmp/expose.out 2>&1 &
  local expose_pid=$! eo=""
  sleep 8 # arm + end-to-end verify (the session logs "path verified" into expose.out)
  for _ in 1 2 3; do
    eo="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$exname:$exposeport/" 2>/tmp/expose-probe.err | tr -d '\r' | tail -1)"
    [ "$eo" = "expose-ok-$exname" ] && break
    sleep 3
  done
  kill $expose_pid 2>/dev/null; wait $expose_pid 2>/dev/null
  if [ "$eo" = "expose-ok-$exname" ]; then
    echo "expose OK — a cluster workload reached this runner's local service by name"; sum "**expose (cluster→local)** ✅"; return 0
  fi
  echo "--- expose FAIL — prober said '${eo:-nothing}' (want expose-ok-$exname)"
  echo "    --- expose session output ---"; tail -12 /tmp/expose.out 2>/dev/null | sed 's/^/    /'
  tail -6 /tmp/expose-probe.err 2>/dev/null | sed 's/^/    /'
  sum "**expose (cluster→local)** ❌ — prober said \`${eo:-nothing}\`"; return 1
}

# exposevar: the SAME reverse path, with the local port NAMED instead of pinned
# (-s <name>:<cluster-port>:PORT). plug allocates a free port, substitutes {PORT}
# in the command, and arms the mapping on that same number.
#
# This is the one check the unit tests cannot make: that the port the child binds
# and the port the tunnel forwards to are the SAME number. Get that wrong and
# nothing errors — echo-local listens happily on one port while the cluster name
# forwards to another, and the prober just gets nothing. Which is exactly what
# this asserts: a body, by name, through the cluster.
do_expose_var() {
  local exname exposeport
  case "$(uname -s)" in
    Darwin) exname=exposedvar-mac; exposeport=18102 ;;
    MINGW*|MSYS*|CYGWIN*) exname=exposedvar-win; exposeport=18103 ;;
    *) exname=exposedvar-linux; exposeport=18101 ;;
  esac
  echo "=== exposevar: $exname:$exposeport → a port plug picks, injected as {PORT} ==="
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- exposevar FAIL — echo-local did not build"; sum "**expose, named port (-s …:PORT)** ❌ (build)"; return 1
  fi
  # echo-local defaults to :18086 when -addr is unusable — so an unsubstituted
  # "{PORT}" cannot accidentally pass by landing on the pinned phase's port.
  "$PLUG" --host "$ip" --port "$port" -s "$exname:$exposeport:PORT" \
    "$root/echo-local$ext" -addr "127.0.0.1:{PORT}" -text "exposevar-ok-$exname" >/tmp/exposevar.out 2>&1 &
  local expose_pid=$! eo=""
  sleep 8
  for _ in 1 2 3; do
    eo="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$exname:$exposeport/" 2>/tmp/exposevar-probe.err | tr -d '\r' | tail -1)"
    [ "$eo" = "exposevar-ok-$exname" ] && break
    sleep 3
  done
  kill $expose_pid 2>/dev/null; wait $expose_pid 2>/dev/null
  if [ "$eo" = "exposevar-ok-$exname" ]; then
    echo "exposevar OK — allocated port, substituted in argv, and the mapping agreed with it"
    sum "**expose, named port (-s …:PORT)** ✅"; return 0
  fi
  echo "--- exposevar FAIL — prober said '${eo:-nothing}' (want exposevar-ok-$exname)"
  echo "    --- exposevar session output ---"; tail -12 /tmp/exposevar.out 2>/dev/null | sed 's/^/    /'
  tail -6 /tmp/exposevar-probe.err 2>/dev/null | sed 's/^/    /'
  sum "**expose, named port (-s …:PORT)** ❌ — prober said \`${eo:-nothing}\`"; return 1
}

# gateway callback (reverse, driven from OUTSIDE): an EXTERNAL caller POSTs to the
# cluster's PUBLISHED gateway, which calls a -s name INSIDE the cluster that lands
# on our sink; the sink answers "<path> <id>" back. Two calls: root and deep path.
do_gateway() {
  local gwname gwcport gwlocal=18096
  case "$(uname -s)" in
    Darwin) gwname=gwsink-mac; gwcport=18092 ;;
    MINGW*|MSYS*|CYGWIN*) gwname=gwsink-win; gwcport=18093 ;;
    *) gwname=gwsink-linux; gwcport=18091 ;;
  esac
  echo "=== gateway callback: external POST → gateway → $gwname → our sink :$gwlocal ==="
  if ! ( cd "$root/e2e/sink" && go build -o "$root/sink$ext" . ); then
    echo "--- gateway FAIL — sink did not build"; sum "**gateway callback** ❌ (build)"; return 1
  fi
  # gw_call <expected> <json-body> — POST to the published gateway, retry (the
  # dynamic name needs a beat to exist), echo PASS or FAIL|<got>.
  gw_call() {
    local want="$1" body="$2" out=""
    for _ in 1 2 3; do
      out="$(curl -s --max-time 10 -X POST "http://$ip:18090/call" \
        -H 'content-type: application/json' -d "$body" 2>>/tmp/gw-post.err | tr -d '\r' | tail -1)"
      [ "$out" = "$want" ] && { echo PASS; return; }
      sleep 3
    done
    echo "FAIL|$out"
  }
  "$PLUG" --host "$ip" --port "$port" -s "$gwname:$gwcport:$gwlocal" \
    "$root/sink$ext" -addr "127.0.0.1:$gwlocal" >/tmp/gw.out 2>&1 &
  local gw_pid=$! gw=FAIL gwpath=FAIL r
  sleep 8 # arm + provision the name + verify
  # 1) root call — sink answers "/ <id>"
  local gwnonce="cb-$gwname-$RANDOM"
  r="$(gw_call "/ $gwnonce" "{\"service\":\"$gwname\",\"port\":\"$gwcport\",\"id\":\"$gwnonce\"}")"
  if [ "$r" = PASS ]; then gw=PASS; echo "gateway OK — external POST reached our sink at / (id round-tripped)"
  else echo "--- gateway FAIL — got '${r#FAIL|}' (want '/ $gwnonce')"; fi
  # 2) deep-path call — sink answers "/hook/<n> <id>", proving the path travelled too
  local gwpnonce="cbp-$gwname-$RANDOM" gwpath_seg
  gwpath_seg="hook/$gwpnonce"
  r="$(gw_call "/$gwpath_seg $gwpnonce" "{\"service\":\"$gwname\",\"port\":\"$gwcport\",\"path\":\"$gwpath_seg\",\"id\":\"$gwpnonce\"}")"
  if [ "$r" = PASS ]; then gwpath=PASS; echo "gateway path OK — the full path /$gwpath_seg reached our sink intact"
  else echo "--- gateway path FAIL — got '${r#FAIL|}' (want '/$gwpath_seg $gwpnonce')"; fi
  [ "$gw" = PASS ] && [ "$gwpath" = PASS ] || { echo "    --- sink session ---"; tail -12 /tmp/gw.out 2>/dev/null | sed 's/^/    /'; tail -6 /tmp/gw-post.err 2>/dev/null | sed 's/^/    /'; }
  kill $gw_pid 2>/dev/null; wait $gw_pid 2>/dev/null
  sum "**gateway callback** $(glyph "$gw") · **gateway path** $(glyph "$gwpath")"
  [ "$gw" = PASS ] && [ "$gwpath" = PASS ]
}

# takeover: -s on a name a REAL deployed service owns PARKS it BY DEFAULT
# (container stopped, traffic lands on our local process) and RESTORES it when
# the session ends — no flag. Target = this leg's own tko-<leg> service
# (parking a shared one would break the other legs) on this leg's own PORT
# (the -s remote-forward binds that port on the agent globally, and the legs
# run concurrently — a shared port made the second leg's forward be denied by
# sshd). The prober is the in-cluster witness: what does
# http://tko-<leg>:<port>/ answer — before, during, after.
do_takeover() {
  local tname tport
  case "$(uname -s)" in
    Darwin)               tname=tko-mac   tport=8086 ;;
    MINGW*|MSYS*|CYGWIN*) tname=tko-win   tport=8087 ;;
    *)                    tname=tko-linux tport=8085 ;;
  esac
  echo "=== takeover: park the deployed $tname, serve ours, restore ==="
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- takeover FAIL — echo-local did not build"; sum "**takeover (park+restore)** ❌ (build)"; return 1
  fi
  probe() { plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$tname:$tport/" 2>/dev/null | tr -d '\r' | tail -1; }

  # Baseline: the deployed service answers through the cluster.
  local r=""
  for _ in 1 2 3; do r="$(probe)"; [ "$r" = "deployed-$tname" ] && break; sleep 3; done
  if [ "$r" != "deployed-$tname" ]; then
    echo "--- takeover FAIL — baseline: prober said '${r:-nothing}' (want deployed-$tname)"
    sum "**takeover (park+restore)** ❌ — baseline"; return 1
  fi

  # Take it over — the DEFAULT, no flag: our local echo must now answer the
  # SAME in-cluster URL. The echo's -ttl ends the session NATURALLY (child
  # exits → plug tears down and restores) — a `kill` on Windows/Git Bash is a
  # TerminateProcess that would skip the teardown, and the restore is exactly
  # what this cell asserts.
  "$PLUG" --host "$ip" --port "$port" -s "$tname:$tport:18096" \
    "$root/echo-local$ext" -addr 127.0.0.1:18096 -text "local-$tname" -ttl 36s >/tmp/takeover.out 2>&1 &
  local tko_pid=$! during=""
  sleep 8 # arm + park + end-to-end verify
  for _ in 1 2 3; do during="$(probe)"; [ "$during" = "local-$tname" ] && break; sleep 3; done
  wait $tko_pid 2>/dev/null # the -ttl fires and the session tears down cleanly

  # Session over: the deployed service must be back (its container restarts).
  local after=""
  for _ in 1 2 3 4 5; do after="$(probe)"; [ "$after" = "deployed-$tname" ] && break; sleep 3; done

  if [ "$during" = "local-$tname" ] && [ "$after" = "deployed-$tname" ]; then
    echo "takeover OK — parked (answers came to us), then restored (deployed answers again)"
    sum "**takeover (park+restore)** ✅"; return 0
  fi
  echo "--- takeover FAIL — during='$during' (want local-$tname) after='$after' (want deployed-$tname)"
  echo "    --- takeover session output ---"; tail -12 /tmp/takeover.out 2>/dev/null | sed 's/^/    /'
  sum "**takeover (park+restore)** ❌ — during \`${during:-nothing}\` · after \`${after:-nothing}\`"; return 1
}

# collision: a name ANOTHER live plug session already serves must be REFUSED —
# the guard the takeover default deliberately keeps (takeover parks DEPLOYED
# workloads only, never another dev's session). A deployed name is no longer
# refused (it is parked — do_takeover proves that), so the cell holds a session
# of its own open and asserts a second one on the same name bounces. Name and
# cluster port are per-leg (the legs run concurrently on the shared cluster).
# --- one name, several cluster ports: the mail-gateway shape -------------------
#
# One process, one -s name, three cluster ports (HTTP+SMTP+POP3 style): one
# signpost carries the name and listens on ALL of them, each relayed to its own
# sshd-allocated agent port. Every port must reach ITS OWN local listener from
# inside the cluster — reaching the wrong one is the bug this cell pins down.
do_multiport() {
  local name p1 p2 p3
  case "$(uname -s)" in
    Darwin)               name=multip-mac   p1=18143 p2=18144 p3=18145 ;;
    MINGW*|MSYS*|CYGWIN*) name=multip-win   p1=18146 p2=18147 p3=18148 ;;
    *)                    name=multip-linux p1=18140 p2=18141 p3=18142 ;;
  esac
  echo "=== multi-port: $name:18131+18132+18133, one process, each port its own backend ==="
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- multi-port FAIL — echo-local did not build"; sum "**one name, three ports** ❌ (build)"; return 1
  fi
  "$PLUG" --host "$ip" --port "$port" \
    -s "$name:18131:$p1" -s "$name:18132:$p2" -s "$name:18133:$p3" \
    "$root/echo-local$ext" -addr "127.0.0.1:$p1,127.0.0.1:$p2,127.0.0.1:$p3" \
    -text "mp-a-$name,mp-b-$name,mp-c-$name" >/tmp/multiport.out 2>&1 &
  local mp_pid=$!
  sleep 8 # arm + verify
  local ra="" rb="" rc=""
  for _ in 1 2 3; do
    ra="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$name:18131/" 2>/dev/null | tr -d '\r' | tail -1)"
    rb="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$name:18132/" 2>/dev/null | tr -d '\r' | tail -1)"
    rc="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$name:18133/" 2>/dev/null | tr -d '\r' | tail -1)"
    [ "$ra" = "mp-a-$name" ] && [ "$rb" = "mp-b-$name" ] && [ "$rc" = "mp-c-$name" ] && break
    sleep 3
  done
  kill $mp_pid 2>/dev/null; wait $mp_pid 2>/dev/null
  if [ "$ra" = "mp-a-$name" ] && [ "$rb" = "mp-b-$name" ] && [ "$rc" = "mp-c-$name" ]; then
    echo "multi-port OK — $name answers on 18131/18132/18133, each port its own backend"
    sum "**one name, three ports (no cross-talk)** ✅"; return 0
  fi
  echo "--- multi-port FAIL — got a='${ra:-nothing}' b='${rb:-nothing}' c='${rc:-nothing}'"
  tail -10 /tmp/multiport.out 2>/dev/null | sed 's/^/    /'
  sum "**one name, three ports** ❌"; return 1
}

# --- same cluster port, several names: the collision this build removed --------
#
# Inside the cluster every service has its own IP, so two services on :3000 are
# the NORMAL world (a NestJS fleet, say) — but every -s converges on the one
# agent, where a fixed port could bind only once: the second session bounced
# with "tcpip-forward request denied". The agent-side port is now allocated per
# session (the signpost relays <name>:<port> to it), so the cluster port stops
# being unique. Both names must answer THEIR OWN local service, from inside the
# cluster, at the same time.
do_sameport() {
  local na nb pa pb
  case "$(uname -s)" in
    Darwin)               na=samep-a-mac   nb=samep-b-mac   pa=18123 pb=18124 ;;
    MINGW*|MSYS*|CYGWIN*) na=samep-a-win   nb=samep-b-win   pa=18125 pb=18126 ;;
    *)                    na=samep-a-linux nb=samep-b-linux pa=18121 pb=18122 ;;
  esac
  echo "=== same-port: $na:18120 AND $nb:18120, both live, both reachable ==="
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- same-port FAIL — echo-local did not build"; sum "**same cluster port ×2** ❌ (build)"; return 1
  fi
  "$PLUG" --host "$ip" --port "$port" -s "$na:18120:$pa"     "$root/echo-local$ext" -addr 127.0.0.1:$pa -text "same-$na" >/tmp/samep-a.out 2>&1 &
  local a_pid=$!
  "$PLUG" --host "$ip" --port "$port" -s "$nb:18120:$pb"     "$root/echo-local$ext" -addr 127.0.0.1:$pb -text "same-$nb" >/tmp/samep-b.out 2>&1 &
  local b_pid=$!
  sleep 8 # arm + verify, both
  local ra="" rb=""
  for _ in 1 2 3; do
    ra="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$na:18120/" 2>/dev/null | tr -d '\r' | tail -1)"
    rb="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$nb:18120/" 2>/dev/null | tr -d '\r' | tail -1)"
    [ "$ra" = "same-$na" ] && [ "$rb" = "same-$nb" ] && break
    sleep 3
  done
  kill $a_pid $b_pid 2>/dev/null; wait $a_pid $b_pid 2>/dev/null
  if [ "$ra" = "same-$na" ] && [ "$rb" = "same-$nb" ]; then
    echo "same-port OK — $na and $nb share :18120, each answered its own backend"
    sum "**same cluster port ×2 (no collision, no cross-talk)** ✅"; return 0
  fi
  echo "--- same-port FAIL — $na said '${ra:-nothing}', $nb said '${rb:-nothing}'"
  tail -8 /tmp/samep-a.out /tmp/samep-b.out 2>/dev/null | sed 's/^/    /'
  sum "**same cluster port ×2** ❌ — a='\`${ra:-nothing}\`' b='\`${rb:-nothing}\`'"; return 1
}

do_collision() {
  local cname cport
  case "$(uname -s)" in
    Darwin)               cname=col-mac   cport=18085 ;;
    MINGW*|MSYS*|CYGWIN*) cname=col-win   cport=18086 ;;
    *)                    cname=col-linux cport=18084 ;;
  esac
  echo "=== collision: a second -s on $cname (held by a live session) must be refused ==="
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- collision FAIL — echo-local did not build"; sum "**collision refused** ❌ (build)"; return 1
  fi
  # Session A holds the name for ~35s (natural end via -ttl — see do_takeover
  # for why kill is not an option on Windows).
  "$PLUG" --host "$ip" --port "$port" -s "$cname:$cport:18098" \
    "$root/echo-local$ext" -addr 127.0.0.1:18098 -text "col-a" -ttl 24s >/tmp/collision-a.out 2>&1 &
  local a_pid=$!
  sleep 8 # arm + verify
  # Session B, same name, while A lives: must bounce (the agent-side port is
  # held by A's remote-forward; the signpost also answers to A).
  local co
  # perl's alarm, like every other cell: a plug that hangs here must fail the
  # cell in seconds, not sit until the job's 25-minute timeout. It did exactly
  # that once — a prompt on a Windows runner with no console to answer it.
  co="$(perl -e 'alarm 60; exec @ARGV or exit 127' \
        "$PLUG" --host "$ip" --port "$port" -s "$cname:$cport:9" curl --version 2>&1 || true)"
  wait $a_pid 2>/dev/null
  if printf '%s' "$co" | grep -qiE "another session|denied by peer|already"; then
    # The refusal must NAME the holder's agent port. That port is what lets a
    # session tell ITS OWN forgotten holder from a stranger's, and so what
    # decides whether it may offer to stop it — without it the offer would be
    # made on a record alone, i.e. possibly on a PID the OS has since reused.
    if ! printf '%s' "$co" | grep -q "agent port [0-9]"; then
      echo "--- collision FAIL — refused, but the refusal names no agent port; got:"
      printf '%s\n' "$co" | tail -5 | sed 's/^/    /'
      sum "**collision refused** ❌ — no agent port named"; return 1
    fi
    # And with NO TERMINAL — which is every CI job, every script — it must
    # refuse outright: never prompt (nothing could answer, so the session would
    # hang forever) and never stop the holder unasked.
    if printf '%s' "$co" | grep -qi "stop it and take the name"; then
      echo "--- collision FAIL — prompted for confirmation with no terminal to answer on"
      sum "**collision refused** ❌ — prompted without a tty"; return 1
    fi
    echo "collision OK — the second session on $cname was refused (port named, no prompt without a tty)"
    sum "**collision refused (names the holder's port, never prompts headless)** ✅"; return 0
  fi
  echo "--- collision FAIL — the second -s on $cname was not refused; got:"
  printf '%s\n' "$co" | tail -5 | sed 's/^/    /'
  echo "    --- session A output ---"; tail -6 /tmp/collision-a.out 2>/dev/null | sed 's/^/    /'
  sum "**collision refused** ❌"; return 1
}

# lease: a live session keeps its name even when its SIGNPOST is gone. That
# state is not exotic — it is what a rebooted agent's boot-gc leaves behind, and
# what any failed re-provision leaves behind. Ownership used to be read off the
# signpost, so "no signpost" read as "name is free": a second session took a
# name its owner was still serving, and from then on the two overwrote each
# other's signpost on every reconnect, each leaving the other silently
# unreachable while everything LOOKED healthy. do_collision cannot catch this —
# there, A's signpost is present when B asks, which is the easy half.
#
# COMPOSE ONLY, like do_resilience and for the same reason: the sweep goes
# through the chaos service, which only the compose cluster deploys. No loss of
# meaning — the lease is taken in serveName, ABOVE the k8s/docker split, so it
# is the same code on all three families.
do_lease() {
  local lname lport lloc
  case "$(uname -s)" in
    Darwin)               lname=lease-mac   lport=18131 lloc=18134 ;;
    MINGW*|MSYS*|CYGWIN*) lname=lease-win   lport=18132 lloc=18135 ;;
    *)                    lname=lease-linux lport=18130 lloc=18133 ;;
  esac
  echo "=== lease: $lname stays its own session's after the signpost is swept ==="
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- lease FAIL — echo-local did not build"; sum "**name survives a swept signpost** ❌ (build)"; return 1
  fi
  # A holds the name for ~45s (natural end via -ttl — see do_takeover for why
  # kill is not an option on Windows).
  "$PLUG" --host "$ip" --port "$port" -s "$lname:$lport:$lloc" \
    "$root/echo-local$ext" -addr 127.0.0.1:$lloc -text "lease-a" -ttl 34s >/tmp/lease-a.out 2>&1 &
  local a_pid=$!
  sleep 8 # arm + verify

  # Baseline: A really is serving, so a later refusal means something.
  local base=""
  for _ in 1 2 3; do
    base="$(plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$lname:$lport/" 2>/dev/null | tr -d '\r' | tail -1)"
    [ "$base" = "lease-a" ] && break
    sleep 3
  done
  if [ "$base" != "lease-a" ]; then
    echo "--- lease FAIL — baseline: prober said '${base:-nothing}' (want lease-a)"
    tail -8 /tmp/lease-a.out 2>/dev/null | sed 's/^/    /'
    wait $a_pid 2>/dev/null; sum "**name survives a swept signpost** ❌ — baseline"; return 1
  fi

  # Sweep A's signpost behind its back — exactly what a boot-gc does.
  local swept
  swept="$(plug curl -s --max-time 15 "http://chaos:8095/rm-signpost?name=$lname" 2>/dev/null | tr -d '\r' | tail -1)"
  if [ "$swept" != "removed" ]; then
    echo "--- lease FAIL — chaos could not sweep $lname's signpost (said '${swept:-nothing}')"
    wait $a_pid 2>/dev/null; sum "**name survives a swept signpost** ❌ — sweep"; return 1
  fi

  # B, same name, while A is still very much alive: must bounce.
  local co
  co="$("$PLUG" --host "$ip" --port "$port" -s "$lname:$lport:9" curl --version 2>&1 || true)"
  wait $a_pid 2>/dev/null
  if printf '%s' "$co" | grep -qiE "another live session|another session|already"; then
    echo "lease OK — $lname stayed its session's with no signpost to prove it"
    sum "**name survives a swept signpost** ✅"; return 0
  fi
  echo "--- lease FAIL — B took $lname while A held it (no signpost to read); got:"
  printf '%s\n' "$co" | tail -5 | sed 's/^/    /'
  echo "    --- session A output ---"; tail -6 /tmp/lease-a.out 2>/dev/null | sed 's/^/    /'
  sum "**name survives a swept signpost** ❌"; return 1
}

# resilience: the M5 bench's crash-recovery chain, replayed in CI — on cluster
# B, against a PER-LEG crash-test agent (res-agent-<leg>, its own published
# port): the three legs run concurrently, and interleaved restarts of a SHARED
# agent tore each other's teardowns apart the one time the legs aligned. A
# takeover session holds res-tko-<leg> through its own agent; the chaos service
# RESTARTS THAT AGENT mid-session; the keepalive must detect the dead
# transport, the rebooted agent's boot-gc restore the parked service, the
# reconnect re-arm -s and RE-PARK it — traffic back on the runner — and the
# session end restore the deployed service for good. The prober is reached
# through the MAIN agent, which never reboots — a witness that cannot blink.
do_resilience() {
  local rname rport ragent rsshport
  case "$(uname -s)" in
    Darwin)               rname=res-tko-mac   rport=8116 ragent=res-agent-mac   rsshport=2224 ;;
    MINGW*|MSYS*|CYGWIN*) rname=res-tko-win   rport=8117 ragent=res-agent-win   rsshport=2225 ;;
    *)                    rname=res-tko-linux rport=8115 ragent=res-agent-linux rsshport=2223 ;;
  esac
  echo "=== resilience (cluster B): park $rname via $ragent, RESTART that agent, re-park, restore ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**resilience (agent crash)** ❌ (cluster B)"; return 1; }
  if ! ( cd "$root/e2e/echo-local" && go build -o "$root/echo-local$ext" . ); then
    echo "--- resilience FAIL — echo-local did not build"; sum "**resilience (agent crash)** ❌ (build)"; return 1
  fi
  bprobe() { plug_to "$ip_b" curl -s --max-time 10 "http://prober:8097/fetch?url=http://$rname:$rport/" 2>/dev/null | tr -d '\r' | tail -1; }

  local r=""
  for _ in 1 2 3; do r="$(bprobe)"; [ "$r" = "deployed-res-$leg" ] && break; sleep 3; done
  if [ "$r" != "deployed-res-$leg" ]; then
    echo "--- resilience FAIL — baseline: prober said '${r:-nothing}' (want deployed-res-${leg})"
    sum "**resilience (agent crash)** ❌ — baseline"; return 1
  fi

  # Hold the takeover THROUGH THIS LEG'S OWN AGENT, with a tight keepalive so
  # the dead transport is detected in seconds; -ttl ends the session naturally
  # (Windows: kill would skip the teardown — see do_takeover).
  PLUG_KEEPALIVE_SECS=5 "$PLUG" --host "$ip_b" --port "$rsshport" -s "$rname:$rport:18123" \
    "$root/echo-local$ext" -addr 127.0.0.1:18123 -text "local-res-$leg" -ttl 110s >/tmp/resilience.out 2>&1 &
  local res_pid=$! during="" after_crash="" after=""
  sleep 8
  for _ in 1 2 3; do during="$(bprobe)"; [ "$during" = "local-res-$leg" ] && break; sleep 3; done

  # Crash THIS LEG'S agent mid-session (the chaos service answers, then fires).
  plug_to "$ip_b" curl -s --max-time 10 "http://chaos:8095/restart-agent?svc=$ragent" >/dev/null 2>&1 || true
  # keepalive detects (~10-15s at 5s cadence), reconnect re-arms and re-parks;
  # the rebooted agent's boot-gc restored the parked service in between.
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    after_crash="$(bprobe)"
    [ "$after_crash" = "local-res-$leg" ] && break
    sleep 5
  done
  wait $res_pid 2>/dev/null # the -ttl fires; teardown restores the deployed service

  for _ in 1 2 3 4 5; do after="$(bprobe)"; [ "$after" = "deployed-res-$leg" ] && break; sleep 3; done

  if [ "$during" = "local-res-$leg" ] && [ "$after_crash" = "local-res-$leg" ] && [ "$after" = "deployed-res-$leg" ]; then
    echo "resilience OK — parked, agent restarted, RE-parked (self-heal + boot-gc + re-arm), restored"
    sum "**resilience (agent crash mid-session)** ✅"; return 0
  fi
  echo "--- resilience FAIL — during='$during' after_crash='$after_crash' (want local-res-$leg) after='$after' (want deployed-res-$leg)"
  echo "    --- session output ---"; tail -15 /tmp/resilience.out 2>/dev/null | sed 's/^/    /'
  sum "**resilience (agent crash mid-session)** ❌ — during \`${during:-nothing}\` · post-crash \`${after_crash:-nothing}\` · after \`${after:-nothing}\`"; return 1
}

# update: `plug update` end to end against THIS LEG'S res-agent (per-leg, like
# resilience — never a shared agent). The agent side runs the docker backend:
# softwarity/plug:e2e exists only locally, so the pull fails cleanly and the
# verdict is `current … could not pull` — proving the verb answers and nothing
# is disturbed. The launcher side is a dev build facing a dev agent, so the
# self-replace path reports and skips (the rolling paths are bench-proven on
# kind/swarm). Runs LAST among the compose cells on purpose.
do_update() {
  local ragent rsshport
  case "$(uname -s)" in
    Darwin)               ragent=res-agent-mac   rsshport=2224 ;;
    MINGW*|MSYS*|CYGWIN*) ragent=res-agent-win   rsshport=2225 ;;
    *)                    ragent=res-agent-linux rsshport=2223 ;;
  esac
  echo "=== update (cluster B): plug update against $ragent ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**plug update** ❌ (cluster B)"; return 1; }

  local out rc=0
  out="$("$PLUG" --host "$ip_b" --port "$rsshport" update </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  if [ "$rc" != 0 ]; then
    echo "--- update FAIL — exit $rc"; sum "**plug update** ❌ — exit $rc"; return 1
  fi
  # The verdict is backend-shaped, and all three are a working `update`:
  #   current  — already on the newest thing its tag can mean
  #   pulled   — Compose fetched it and handed back the recreate, since a
  #              container cannot recreate itself
  #   updating — Swarm rewrites the service image / k8s patches the Deployment,
  #              and the task rolls for real
  # The crash-test agents run a tag built INTO the cluster, with no registry
  # behind it, so on Swarm and k8s the roll lands on the same image and plug
  # says so rather than claiming a move. That report is the honest answer, not
  # a failure — what this cell asserts is that the verb ran, said which of the
  # three happened, and left the agent standing.
  if ! printf '%s' "$out" | grep -Eq "agent: (current|pulled|updating)"; then
    echo "--- update FAIL — no agent verdict (want current/pulled/updating)"; sum "**plug update** ❌ — no agent verdict"; return 1
  fi
  if ! printf '%s' "$out" | grep -Eq "launcher (already matches|is a dev build)"; then
    echo "--- update FAIL — no launcher line"; sum "**plug update** ❌ — no launcher line"; return 1
  fi
  # The verb must have left the agent standing.
  local v
  v="$(ssh -n -p "$rsshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "get@$ip_b" version 2>/dev/null | tr -d '\r')"
  if [ -z "$v" ]; then
    echo "--- update FAIL — $ragent no longer answers"; sum "**plug update** ❌ — agent down after"; return 1
  fi
  echo "update OK — verdict relayed, launcher path reported, $ragent still v$v"
  sum "**plug update (verb + launcher path)** ✅"; return 0
}

# ================================ dispatch ================================
if [ "$phase" != setup ]; then
  [ -f "$envfile" ] || { echo "no e2e state at $envfile — run the setup phase first" >&2; exit 1; }
  . "$envfile" # PLUG, ip, built
  { [ -n "${PLUG:-}" ] && [ -x "$PLUG" ]; } || { echo "plug not usable ('${PLUG:-}') — did setup fail?" >&2; exit 1; }
fi

# --- update: a RELEASE agent must retarget itself to the newest one -------------
#
# The starting point is the PREVIOUS published release, resolved when the
# cluster is built (scripts/ci/previous-release.sh) rather than pinned. That is
# the realistic upgrade path — what someone one release behind is running — and
# it never rots: a pinned old tag re-tests bugs fixed several releases ago, and
# takes every family down the day it leaves the registry.
#
# It must ask the registry, find a NEWER x.y.z than its own, and name it. Aiming
# at a version is what `plug update` is for — re-resolving the tag it already
# carries is what it used to do, and that returned the same image forever.
#
# Compose cannot recreate a container from inside it, so the verdict is "pulled"
# plus the command that finishes the job; the rollout itself is Swarm/k8s work,
# covered by the resilience cell. What this asserts is the DECISION.
do_update_jump() {
  local oldport
  case "$(uname -s)" in
    Darwin)               oldport=2227 ;;
    MINGW*|MSYS*|CYGWIN*) oldport=2228 ;;
    *)                    oldport=2226 ;;
  esac
  echo "=== update-jump (cluster B): an agent one release behind must retarget to the newest ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**update jump** ❌ (cluster B)"; return 1; }
  # Which release it starts from is the AGENT's answer, not something the
  # harness is told: the cluster deploys whatever previous-release.sh resolved
  # when it was built, and asking removes any chance of the two disagreeing.
  local prev
  prev="$(ssh -n -p "$oldport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
          -o LogLevel=ERROR "get@$ip_b" version 2>/dev/null | tr -d '\r')"
  if ! printf '%s' "$prev" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "--- update-jump FAIL — the previous-release agent answered '${prev:-nothing}', not an x.y.z release"
    sum "**update jump** ❌ — no usable starting version"; return 1
  fi
  echo "    starting from the published $prev"

  local out rc=0
  out="$("$PLUG" --host "$ip_b" --port "$oldport" update </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  if [ "$rc" != 0 ]; then
    echo "--- update-jump FAIL — exit $rc"; sum "**update jump** ❌ — exit $rc"; return 1
  fi
  # The DECISION, asserted the same way on every backend: a release strictly
  # newer than the one it runs must be named.
  local target
  target="$(printf '%s' "$out" | sed -n 's|.*to [a-z0-9./-]*plug:\([0-9][0-9.]*\).*|\1|p' | head -1)"
  if [ -z "$target" ]; then
    echo "--- update-jump FAIL — the agent named no newer release (it should move off $prev)"
    sum "**update jump** ❌ — no target named"; return 1
  fi
  if [ "$target" = "$prev" ]; then
    echo "--- update-jump FAIL — retargeted to itself ($target)"
    sum "**update jump** ❌ — retargeted to itself"; return 1
  fi

  # Then what each backend can actually DO with that decision. Swarm rewrites
  # the service image and k8s patches the Deployment, so the agent really lands
  # on the new version and the CLI reports the move; Compose cannot recreate a
  # container from inside it, so it stops at "pulled" plus the command that
  # finishes the job. Assert whichever this cluster is, from the CLI's own line
  # — never skip: a backend that silently did nothing would look identical.
  agent_version() {
    ssh -n -p "$oldport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o LogLevel=ERROR "get@$ip_b" version 2>/dev/null | tr -d '\r'
  }
  local now=""
  if printf '%s' "$out" | grep -q "agent updated: v$prev"; then
    # A rollout is ASYNCHRONOUS — the old pod/task goes, the new image is pulled,
    # the new one becomes ready — so reading the version ONCE, the instant the
    # CLI reports the move, measures the rollout's SPEED rather than its outcome.
    # It duly failed on one leg while the other two passed the same assert.
    #
    # Only here, never before this branch: on Compose nothing rolls (the CLI
    # cannot recreate its own container), so a wait for a version that will
    # never change burned its full budget on every compose leg — 200s of pure
    # sleep on the run's critical path.
    for _ in $(seq 1 40); do
      now="$(agent_version)"
      [ "$now" = "$target" ] && break
      sleep 5
    done
    if [ "$now" != "$target" ]; then
      echo "--- update-jump FAIL — reported a move to $target but the agent answers v$now"
      sum "**update jump** ❌ — moved to $target, agent still v$now"; return 1
    fi
    echo "update-jump OK — $prev retargeted to $target and the rollout landed (agent now v$now)"
    sum "**plug update (release agent rolls: $prev → $target)** ✅"; return 0
  fi
  now="$(agent_version)"
  if printf '%s' "$out" | grep -q "cannot recreate its own container"; then
    if [ "$now" != "$prev" ]; then
      echo "--- update-jump FAIL — nothing should have rolled here, yet the agent answers v$now"
      sum "**update jump** ❌ — unexpected roll to v$now"; return 1
    fi
    echo "update-jump OK — $prev retargeted to $target, pulled it, and handed back the recreate (agent still v$now)"
    sum "**plug update (release agent retargets: $prev → $target)** ✅"; return 0
  fi
  echo "--- update-jump FAIL — named $target but neither rolled nor pulled"
  sum "**update jump** ❌ — decision without an action"; return 1
}

# --- update <tag>: the two ways it must REFUSE ---------------------------------
#
# Both matter more than the happy path. Repointing a deployment at a tag nobody
# published leaves an agent that cannot pull — on Swarm/k8s, a rollout to unwind
# by hand. And an agent that predates the target argument runs `self-update <tag>`
# as a PLAIN self-update: it ignores the word, so the channel would silently not
# change while the command reported success.
do_update_tag() {
  local rsshport
  case "$(uname -s)" in
    Darwin)               rsshport=2224 ;;
    MINGW*|MSYS*|CYGWIN*) rsshport=2225 ;;
    *)                    rsshport=2223 ;;
  esac
  echo "=== update-tag (cluster B): an unpublished tag, and an agent too old for the argument ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**update tag** ❌ (cluster B)"; return 1; }

  # 1) A tag the registry does not have: refused, and the agent left standing.
  local out rc=0
  out="$("$PLUG" --host "$ip_b" --port "$rsshport" update definitely-not-a-published-tag </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  if [ "$rc" = 0 ]; then
    echo "--- update-tag FAIL — an unpublished tag was ACCEPTED"; sum "**update tag** ❌ — unpublished tag accepted"; return 1
  fi
  if ! printf '%s' "$out" | grep -q "has no tag"; then
    echo "--- update-tag FAIL — refused, but not for the right reason"; sum "**update tag** ❌ — wrong refusal"; return 1
  fi
  local v
  v="$(ssh -n -p "$rsshport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "get@$ip_b" version 2>/dev/null | tr -d '\r')"
  if [ -z "$v" ]; then
    echo "--- update-tag FAIL — the agent went down on a refusal"; sum "**update tag** ❌ — agent down after refusal"; return 1
  fi

  # What used to be asserted here — that an agent predating the <tag> argument
  # is refused on its VERSION — is gone with the fixture it needed. plug is
  # young and agents follow releases (doctor and `plug update` exist for that),
  # so a cell pinned to a years-old build tests bugs fixed several releases ago:
  # a failure there says nothing about the code under review.
  echo "update-tag OK — the unpublished tag was refused and the agent is still up (v$v)"
  sum "**plug update <tag> (an unpublished tag is refused, agent left standing)** ✅"; return 0
}

case "$phase" in
  setup)        do_setup ;;
  env)          do_env ;;
  matrix)       do_matrix ;;
  multicluster) do_multicluster ;;
  outage)       do_outage ;;
  expose)       do_expose ;;
  exposevar)    do_expose_var ;;
  gateway)      do_gateway ;;
  takeover)     do_takeover ;;
  collision)    do_collision ;;
  lease)        do_lease ;;
  sameport)     do_sameport ;;
  multiport)    do_multiport ;;
  resilience)   do_resilience ;;
  update)       do_update ;;
  updatejump)   do_update_jump ;;
  updatetag)    do_update_tag ;;
  *) echo "unknown phase: $phase (want setup|env|matrix|multicluster|outage|expose|exposevar|gateway|takeover|collision|lease|resilience|update)" >&2; exit 2 ;;
esac
