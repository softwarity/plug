#!/usr/bin/env bash
# The mesh e2e, split into PHASES so each family is its OWN CI step (its own
# green/red in the run view — no more "e2e failed, go dig the log") while sharing
# ONE install. Runs NATIVELY on this runner (Linux, macOS, or Windows via Git
# Bash): plug is INSTALLED FROM THE CLUSTER (the real one-liner, real privilege
# grant), then every family runs that installed plug against a REAL cluster BY
# NAME over the Tailscale mesh.
#
#   e2e-matrix.sh <phase> <cluster-a> <cluster-b> [port]
#   phases: setup env matrix multicluster outage expose gateway collision
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
# 140×3s ≈ 7min: the cluster run builds the agent image in its own job and ships
# it to the serve job as an artifact before anything joins the mesh.
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
# plug's launcher → core → shim chain untouched).
do_env() {
  echo "=== env passthrough ==="
  local ev
  ev="$(PLUG_E2E_CANARY=canary-42 perl -e 'alarm 45; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" $serve \
    bash -c 'echo "$PLUG_E2E_CANARY"' 2>/dev/null | tr -d '\r' | tail -1)"
  if [ "$ev" = "canary-42" ]; then
    echo "env OK — the child sees the caller's variables"; sum "**env passthrough** ✅"; return 0
  fi
  echo "--- env FAIL — child saw '${ev:-<nothing>}' (want canary-42)"; sum "**env passthrough** ❌"; return 1
}

# protocol matrix: every language client, UNDER plug, reaches each cluster
# service BY NAME over the mesh. The 4×8 grid is rendered into the step summary.
do_matrix() {
  echo "=== matrix: each client UNDER plug → service by name ==="
  local fails=0 results="" entry proto target l r out _attempt
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
  local expect_a="${peer#plug-cluster-}" expect_b="${peer_b#plug-cluster-}"
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
  b_out="$(plug_to "$ip_b" curl -sS http://ident:5678 2>/tmp/mc-b.err || true)" # ...then hit B DURING A
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

# collision: a name a real cluster service already owns must be REFUSED. httpbin
# is a live service here, so `plug -s httpbin` has to fail loud — the guard that
# stops a dev from shadowing a name in use. The command never runs (plug bails at
# provisioning), so its exact form is irrelevant.
do_collision() {
  echo "=== collision: plug -s httpbin (a real cluster service) must be refused ==="
  local co
  co="$("$PLUG" --host "$ip" --port "$port" -s httpbin:9998:9 curl --version 2>&1 || true)"
  if printf '%s' "$co" | grep -qiE "owns the name|already resolves|is a service in the cluster|answered by something else"; then
    echo "collision OK — plug refused to shadow the real httpbin"; sum "**collision refused** ✅"; return 0
  fi
  echo "--- collision FAIL — plug did not refuse; got:"; printf '%s\n' "$co" | tail -5 | sed 's/^/    /'
  sum "**collision refused** ❌"; return 1
}

# ================================ dispatch ================================
if [ "$phase" != setup ]; then
  [ -f "$envfile" ] || { echo "no e2e state at $envfile — run the setup phase first" >&2; exit 1; }
  . "$envfile" # PLUG, ip, built
  { [ -n "${PLUG:-}" ] && [ -x "$PLUG" ]; } || { echo "plug not usable ('${PLUG:-}') — did setup fail?" >&2; exit 1; }
fi

case "$phase" in
  setup)        do_setup ;;
  env)          do_env ;;
  matrix)       do_matrix ;;
  multicluster) do_multicluster ;;
  outage)       do_outage ;;
  expose)       do_expose ;;
  gateway)      do_gateway ;;
  collision)    do_collision ;;
  *) echo "unknown phase: $phase (want setup|env|matrix|multicluster|outage|expose|gateway|collision)" >&2; exit 2 ;;
esac
