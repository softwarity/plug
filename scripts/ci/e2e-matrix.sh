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

# All four languages, on every family. The override exists for a bench run, not
# for CI — and there is a reason worth keeping written down.
#
# It was briefly used to cut swarm and k8s down to Go, on the argument that the
# LANGUAGE axis tests the client (four resolvers: the JVM's cache, c-ares in
# Node, libc in Python, Go's own) while the PROTOCOL axis tests the family's
# network — so the two would be orthogonal and the compose legs could carry the
# languages alone. That argument is WRONG, and cost 276s on one leg to be so.
#
# Each language does not merely resolve differently: it brings its OWN
# IMPLEMENTATION of the wire protocol. AMQP is amqp091-go, amqplib, pika and
# com.rabbitmq — four codebases with different framing, heartbeats, pooling and
# write sizes. That traffic crosses the tunnel and then the family's network,
# where a VXLAN overlay's 1450-byte MTU does not answer a driver writing in
# large blocks the way it answers one writing small. Language and family are not
# independent, and nobody had measured that they were.
LANGS="${E2E_LANGS:-go node python java}"
# proto:host:port — the by-name target for each service.
PROTOS="http:httpbin:8080 postgres:postgres:5432 redis:redis:6379 mongo:mongo:27017 amqp:rabbitmq:5672 mqtt:mosquitto:1883 grpc:grpc:50051 websocket:wsserver:8090"

# Which cluster family this run is against, read off the cluster name the
# workflow passes (plug-cluster-… / plug-swarm-… / plug-k8s-…). Every cell runs
# on all three; a couple of them assert something only one backend can give, and
# say so rather than skipping.
case "$peer_b" in
  *-swarm-*|plug-swarm-*) family=swarm ;;
  *-k8s-*|plug-k8s-*)     family=k8s ;;
  *)                      family=compose ;;
esac

# --- OS specifics ---
ext=""; py="python3"
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) ext=".exe"; py="python" ;; esac
SSH_OPTS="-p $port -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes"

# --- prebuilt clients (see scripts/ci/build-clients.sh) -----------------------
# PREBUILT_CLIENTS points at what one Linux runner produced for every leg: the
# Go client and helpers cross-compiled per target, the jar, and node_modules.
# Building those on each leg cost 493s of a 769s setup on Windows, three times
# over, for bytes that are identical between legs.
#
# Unset — a local run, a manual dispatch — and everything builds as it always
# did. That fallback is not decoration: it is how anyone runs this script
# outside CI, so it stays exercised by the bench.
prebuilt="${PREBUILT_CLIENTS:-}"
case "$(uname -s)" in
  Darwin)               gtag=darwin ;;
  MINGW*|MSYS*|CYGWIN*) gtag=windows ;;
  *)                    gtag=linux ;;
esac
case "$(uname -m)" in aarch64|arm64) gtag="$gtag-arm64" ;; *) gtag="$gtag-amd64" ;; esac

# take_prebuilt <name> <destination> — install a cross-compiled binary if it is
# there. Returns 1 when it is not, so every caller keeps its own build path: a
# runner label we did not anticipate finds no binary for its $gtag and rebuilds,
# which is slow but never wrong.
#
# chmod is not belt-and-braces: a GitHub artifact is a zip, and the executable
# bit does not survive the round trip. Without it every cell fails on
# "permission denied" from a file that is plainly there.
take_prebuilt() {
  [ -n "$prebuilt" ] && [ -f "$prebuilt/$1-$gtag$ext" ] || return 1
  cp "$prebuilt/$1-$gtag$ext" "$2" && chmod +x "$2"
}

# helper_bin <name> — put e2e/<name>'s binary at $root/<name>$ext. echo-local is
# rebuilt by NINE cells and sink by one; none of them needs its own compiler.
helper_bin() {
  take_prebuilt "$1" "$root/$1$ext" && return 0
  ( cd "$root/e2e/$1" && go build -o "$root/$1$ext" . )
}

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

# --- wait for a PER-LEG agent to answer on its own published port ---------------
# The resilience cell crashes one of these by design, and the update cells reuse
# the SAME agent right after. Without this they fail on "connection refused" for
# a reason that has nothing to do with what they assert — a cascade seen once on
# a loaded macOS runner, where ubuntu passed the identical cells.
wait_agent() {
  wa_ip="$1"; wa_port="$2"
  for _ in $(seq 1 40); do
    if ssh -n -p "$wa_port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
         -o LogLevel=ERROR -o BatchMode=yes -o ConnectTimeout=5 "get@$wa_ip" version >/dev/null 2>&1; then
      return 0
    fi
    sleep 3
  done
  return 1
}

# agent_state prints WHY an agent is not answering — its container state and its
# own last lines, asked from inside the cluster through the chaos service.
#
# Written after three red cells pointed at one invisible cause: the resilience
# cell restarts an agent by design, and when it did not come back every cell
# using it failed saying only that. Two rounds of guessing at timeouts followed.
# A cell that cannot explain its failure sends whoever reads it looking in the
# wrong place — that is what this exists to stop.
agent_state() {
  as_ip="$1"; as_svc="$2"
  echo "    --- state of $as_svc, from inside the cluster ---"
  plug_to "$as_ip" curl -s --max-time 15 "http://chaos:8095/agent-state?svc=$as_svc" 2>/dev/null \
    | tr -d '\r' | sed 's/^/    /' | tail -35
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
# Two Linux legs now share cluster A — amd64 and arm64 — and they must not share
# a NAME or a port: `-s run-linux` twice is a collision, which is a different
# cell's subject entirely. The architecture is what tells them apart.
case "$(uname -m)" in
  aarch64|arm64) [ "$leg" = linux ] && { leg=linuxarm; sport=18074; } ;;
esac
serve="-s run-${leg}:${sport}:9"   # hyphen only — an underscore is not a valid DNS label

# Per-cell timeout: a client with no timeout of its own must not hang the job.
# perl's alarm is on every runner (incl. Git Bash) and survives exec.
plug_to() { to="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$to" --port "$port" $serve "$@"; }
plug()    { plug_to "$ip" "$@"; }
# plug_serving <-s name:cport:lport> <cmd…> — plug() with a name of OUR choosing
# instead of the leg's single one, so several sessions can run at once. Claiming
# `$serve` twice at the same moment is a collision, which is another cell's
# subject entirely.
plug_serving() { pv="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$ip" --port "$port" $pv "$@"; }
# A free block of cluster ports, four per leg: the cells use up to 18148, and
# the legs are spaced so two of them never overlap (linux 18150, mac 18154,
# win 18158, arm 18162).
mport_base=$(( 18150 + (sport - 18071) * 4 ))
cmd_go()     { echo "$clients/go/eclient$ext"; }
cmd_node()   { echo "node $clients/node/client.js"; }
cmd_python() { echo "$py $clients/python/client.py"; }
cmd_java()   { echo "java -jar $clients/java/target/client.jar"; }

# is_addr: the reading is an IPv4 address, not an error string. Every address
# assertion goes through it — comparing two identical error messages once read
# as "address kept", a test that passed without testing.
is_addr() { case "${1:-}" in ""|*[!0-9.]*) return 1 ;; *) return 0 ;; esac; }
glyph() { case "$1" in PASS) printf "✅" ;; FAIL) printf "❌" ;; SKIP) printf "·" ;; *) printf "?" ;; esac; }
sum()   { echo "$*" >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"; }

# ================================ phases ================================

# setup: the real user flow — install plug FROM the cluster (the installer grants
# the privilege the real way: setcap / setuid helper / SCM SYSTEM service), build
# the four language clients, and record PLUG/ip/built for the family phases.
# why_no_cluster prints the ONE fact that tells the two causes apart: was the
# agent image this cluster runs even published yet?
#
# A ten-legged run said "cluster never became reachable" ten times over, and the
# cause was neither the clusters nor the legs — the amd64 image build had taken
# 18m30 instead of its usual 2m30, so every cluster gave up pulling before it
# existed. From a leg the two states are identical, and reading them the wrong
# way round costs an hour of looking at the wrong thing.
why_no_cluster() {
  [ -n "${AGENT_IMAGE:-}" ] || return 0
  wn_repo="${AGENT_IMAGE#*/}"; wn_repo="${wn_repo%%:*}"; wn_tag="${AGENT_IMAGE##*:}"
  wn_tok="$(curl -s --max-time 20 "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${wn_repo}:pull" \
            | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
  [ -n "$wn_tok" ] || { echo "    (could not ask the registry — no verdict on the image)" >&2; return 0; }
  wn_code="$(curl -s --max-time 20 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $wn_tok" \
             -H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json" \
             "https://registry-1.docker.io/v2/${wn_repo}/manifests/${wn_tag}")"
  if [ "$wn_code" = 200 ]; then
    echo "    $AGENT_IMAGE IS published — so the image is not the reason; look at the cluster run" >&2
  else
    echo "    $AGENT_IMAGE is NOT published yet (registry said $wn_code) — the clusters had nothing to pull." >&2
    echo "    This is the image build being slow, not the cluster or this leg. Re-run once it is up." >&2
  fi
}

do_setup() {
  echo "=== wait for cluster A ($peer:$port) ==="
  ip="$(wait_cluster "$peer")" || { echo "cluster $peer never became reachable" >&2; why_no_cluster; exit 1; }
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
  # Each one takes what the shared build already produced, and falls back to
  # building it. Only python has nothing to take: its wheels are compiled.
  build_go()   { take_prebuilt eclient "$clients/go/eclient$ext" || ( cd "$clients/go" && go build -o "eclient$ext" . ); }
  build_java() {
    [ -n "$prebuilt" ] && [ -f "$prebuilt/client.jar" ] && {
      mkdir -p "$clients/java/target" && cp "$prebuilt/client.jar" "$clients/java/target/client.jar"; return $?; }
    ( cd "$clients/java" && mvn -e -B package ) # no -q: surface the goal on failure
  }
  build_node() {
    [ -n "$prebuilt" ] && [ -f "$prebuilt/node_modules.tar.gz" ] && {
      tar -xzf "$prebuilt/node_modules.tar.gz" -C "$clients/node"; return $?; }
    ( cd "$clients/node" && npm install --omit=dev --no-audit --no-fund )
  }
  # --break-system-packages: macOS runners ship a Homebrew Python that refuses a
  # plain `pip install` (externally-managed-environment).
  build_python() { $py -m pip install --quiet --disable-pip-version-check --user --break-system-packages -r "$clients/python/requirements.txt"; }
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

  # wait_cluster proves the AGENT answers ssh. It says nothing about the
  # SERVICES the cells reach by name — on kind especially, pods are still
  # starting well after the agent is up.
  #
  # That gap was invisible while setup spent 769s building four language
  # clients: everything had long finished settling by the time the first cell
  # ran. Setup takes about a minute now, and `client-only` failed twice in a day
  # with curl code 000 on a cluster that was simply not up yet — a retry inside
  # the cell did not help, because the wait needed is tens of seconds, not five.
  #
  # So wait HERE, once, for the whole run rather than in each early cell. The
  # probe is the same shape as the first assertion that needs it: `-c` reaching
  # httpbin by name. How long it waited is printed on purpose — if that number
  # ever grows, it is the cluster getting slower to come up, and this line is
  # where you would see it rather than a cell failing for a reason of its own.
  echo "=== wait for the cluster's services (an agent answering is not a cluster ready) ==="
  svc_t=0
  while [ "$svc_t" -lt 150 ]; do
    if [ "$(perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$ip" --port "$port" -c \
            curl -s --max-time 10 -o /dev/null -w '%{http_code}' http://httpbin:8080/get 2>/dev/null | tr -d '\r' | tail -1)" = 200 ]; then
      echo "services answering after ${svc_t}s"
      break
    fi
    sleep 10; svc_t=$((svc_t + 10))
  done
  [ "$svc_t" -lt 150 ] || echo "--- services still not answering after ${svc_t}s — the cells will say what that costs"

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

  echo "=== dns relay: a question that is not an address must still be answered ==="
  # plug used to reply NODATA to every SRV, MX, PTR and TXT. On macOS its stub is
  # the resolver for the WHOLE machine while a session runs, so that broke AD
  # clients, mongodb+srv:// URIs and Consul host-wide. The unit tests prove the
  # relay and the no-leak rule with a fake upstream; what only a real machine can
  # show is that a REAL resolver, reached through plug's stub on this OS, still
  # answers. A not-found is the one verdict that fails: nothing but plug can
  # produce it for a name that plainly has MX records.
  if [ -x "$(cmd_go)" ]; then
    local dr dout
    # The WHOLE output, and the verdict looked up in it — not `tail -1`. plug
    # refuses a held name over several lines, and the last of them reads "the
    # holder is on another machine or another account": taken alone it was
    # reported as a DNS relay failure, for a session that never started at all.
    dout="$(plug "$(cmd_go)" dns mx:google.com 2>&1 | tr -d '\r')"
    dr="$(printf '%s\n' "$dout" | grep -m1 '^E2E-OK' || printf '%s\n' "$dout" | tail -1)"
    case "$dr" in
      E2E-OK*) echo "dns relay OK — $dr"; sum "**dns relay (non-A → upstream)** ✅" ;;
      *)
        if printf '%s' "$dout" | grep -q "It frees itself once that session ends"; then
          # A refusal is not a relay verdict. Say which it is, because the two
          # send whoever reads this to opposite places.
          echo "--- dns relay FAIL — plug REFUSED to start: the name is still held, so the relay never ran"
          sum "**dns relay (non-A → upstream)** ❌ — plug refused to start (name still held)"
        else
          echo "--- dns relay FAIL — ${dr:-<nothing>}"
          sum "**dns relay (non-A → upstream)** ❌ — \`${dr:-nothing}\`"
        fi
        printf '%s\n' "$dout" | tail -8 | sed 's/^/    /'
        return 1 ;;
    esac
  else
    echo "dns relay SKIP — the go client was not built on this leg"
    sum "**dns relay (non-A → upstream)** ·"
  fi

  echo "=== client-only (-c): consume the cluster, nothing served ==="
  # The DB-tool shape: no name, no agent port, outbound only.
  #
  # Two attempts, for the same reason do_matrix takes two: the mesh datapath can
  # blip on the first hit. This cell was single-shot and got away with it while
  # setup took 769s — twelve minutes of building clients during which the cluster
  # finished settling. Setup is now ~70s, so the first cell arrives while the
  # stack may still be coming up, and `wait_cluster` only proves the AGENT
  # answers, not that httpbin does. Shortening the run did not create the race;
  # it stopped hiding it.
  # -sS, and curl's stderr KEPT. It was `-s` with `2>/dev/null`, which threw away
  # the one thing worth having: 000 means "no HTTP response", and covers a name
  # that did not resolve, a connection refused and a timeout alike — three
  # different problems that plug answers three different ways. Twice this cell
  # went red on a 000 nobody could attribute, because the cause had been
  # discarded one character at a time.
  local co _try
  for _try in 1 2; do
    co="$(perl -e 'alarm 45; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" -c \
      curl -sS --max-time 10 -o /dev/null -w '%{http_code}' http://httpbin:8080/get 2>/tmp/client-only.err | tr -d '\r' | tail -1)"
    [ "$co" = "200" ] && break
    [ "$_try" = 1 ] && { echo "    (got '${co:-nothing}' — one retry, the datapath may still be settling)"; sleep 5; }
  done
  if [ "$co" = "200" ]; then
    echo "client-only OK — -c reached httpbin by name with nothing served"
    sum "**client-only (-c)** ✅"
  else
    echo "--- client-only FAIL — got '${co:-nothing}' (want 200)"
    echo "    curl said: $(tr -d '\r' < /tmp/client-only.err | tail -3 | tr '\n' ' ')"
    # Which of the three it is decides where to look: an unresolved name is the
    # pre-mint check saying the cluster does not have it; a refusal is the name
    # being there with nothing behind it; a timeout is neither, and would be the
    # only one of the three that points at plug.
    echo "    --- what plug thinks, right now ---"
    # doctor, NOT inside a plug session: it is read-only and needs no datapath,
    # and nesting it in the very session under suspicion would tell us about the
    # wrong thing (and can take longer than the answer is worth).
    perl -e 'alarm 40; exec @ARGV or exit 127' "$PLUG" doctor 2>&1 \
      | grep -iE "resolver|daemon|datapath|agent|profile" | head -8 | sed 's/^/    /'
    sum "**client-only (-c)** ❌ — \`${co:-nothing}\` · $(tr -d '\r' < /tmp/client-only.err | tail -1)"; return 1
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
# matrix_lang <lang> <port-base> <index> — ONE language against all eight
# protocols, under a name of its own so it can run beside the other three.
#
# Prints "RESULT <lang> <proto> PASS|FAIL" lines the caller collects, and any
# diagnosis a failure earns. It runs in a background subshell, so nothing it
# assigns survives: the file it writes is the only thing that comes back.
matrix_lang() {
  ml_l="$1"; ml_base="$2"; ml_i="$3"
  ml_serve="-s run-${leg}-${ml_l}:$(( ml_base + ml_i )):9"
  for ml_entry in $PROTOS; do
    ml_proto="${ml_entry%%:*}"; ml_target="${ml_entry#*:}"
    # 2 attempts: the mesh datapath can blip transiently on the first hit.
    ml_r=FAIL; ml_out=""
    for ml_attempt in 1 2; do
      ml_out="$(plug_serving "$ml_serve" $("cmd_$ml_l") "$ml_proto" "$ml_target" 2>&1)"
      if printf '%s' "$ml_out" | grep -q "E2E-OK"; then ml_r=PASS; break; fi
      sleep 2
    done
    echo "RESULT $ml_l $ml_proto $ml_r"
    [ "$ml_r" = PASS ] && continue
    echo "--- $ml_l / $ml_proto FAIL ---"; printf '%s\n' "$ml_out" | tail -8 | sed 's/^/    /'
    # go-on-mac only: the failure pattern (5/8 pass) rules out a plain "wrong
    # resolver" story — capture, INSIDE a live plug session, what the system
    # resolver config looks like and which resolver path Go actually takes.
    if [ "$ml_l" = go ] && [ "$(uname -s)" = Darwin ]; then
      echo "    --- go/mac TIMED diagnosis (inside a live session) ---"
      ml_host=${ml_target%%:*}
      plug_serving "$ml_serve" bash -c "
        TIMEFORMAT='    [%Rs]'
        echo '--- timed: dscacheutil $ml_host (getaddrinfo path) ---'
        time dscacheutil -q host -a name $ml_host
        echo '--- timed: dig $ml_host.plug @198.18.0.53 (in-stack direct) ---'
        time dig +time=4 +tries=1 +short $ml_host.plug @198.18.0.53
        echo '--- timed: tailscale ping (mesh RTT) ---'
        time tailscale ping -c 2 $peer
        echo '--- timed: client, FORCED pure-Go resolver (resolv.conf path) ---'
        time env GODEBUG=netdns=go+1 perl -e 'alarm 15; exec @ARGV' $clients/go/eclient$ext $ml_proto $ml_target
        echo '--- timed: client, default cgo resolver ---'
        time env GODEBUG=netdns=2 perl -e 'alarm 15; exec @ARGV' $clients/go/eclient$ext $ml_proto $ml_target
      " 2>&1 | head -60 | sed 's/^/    diag| /'
    fi
  done
}

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
  # The four languages run AT THE SAME TIME, each with its own name and port.
  #
  # Measured before this: 187s for 32 invocations — 5.8s each — of which the
  # protocol exchange is a sliver. What costs is starting a session: the SSH
  # connection, the datapath, provisioning the name. Thirty-two of those, one
  # after another, to run eight protocols four ways.
  #
  # Each language keeps its protocols in ORDER (its eight run one after another,
  # against one cluster) — what overlaps is the four languages, which have
  # nothing to say to each other. Output goes to a file per language and is
  # printed after the join, or four concurrent failures would interleave into
  # something nobody can read.
  local li=0 pids="" pid
  for l in $LANGS; do
    matrix_lang "$l" "$mport_base" "$li" > "/tmp/matrix-$l.log" 2>&1 &
    pids="$pids $!"
    li=$((li + 1))
  done
  for pid in $pids; do wait "$pid"; done
  for l in $LANGS; do
    results="$results$(sed -n 's/^RESULT //p' "/tmp/matrix-$l.log")
"
    grep -v '^RESULT ' "/tmp/matrix-$l.log" || true   # what it had to say, if anything
  done
  # Counted from the collected lines, not incremented as we go: the four
  # languages ran in subshells, and a counter bumped there dies with them.
  fails=$(printf '%s\n' "$results" | grep -c ' FAIL$' || true)
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
  # A zero count is not a pass on its own. `grep -c` on empty input prints 0 and
  # exits 1, the `|| true` swallows that status, and the verdict below succeeds:
  # this cell - four languages by eight protocols, the heaviest of the suite -
  # could go green having run nothing at all. A killed subshell, a full /tmp, or
  # matrix_lang dying before its first iteration all produce exactly that.
  #
  # So count what came back and require the full grid. The `missing` guard above
  # only covers clients that failed to BUILD.
  local want got
  want=$(( $(printf '%s\n' $LANGS | wc -l) * $(printf '%s\n' $PROTOS | wc -l) ))
  got=$(printf '%s\n' "$results" | grep -cE ' (PASS|FAIL)$' || true)
  echo "=== matrix: $got/$want result(s), $fails failure(s) ==="
  if [ "$got" -ne "$want" ]; then
    echo "    only $got of $want cells reported - the grid did not run to completion"
    return 1
  fi
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
  # Named from $leg, not from uname: two Linux legs (amd64 and arm64) would
  # otherwise claim the same name on a shared cluster — measured, and it took
  # the amd64 leg down with it. The port stays per-OS: it is local to this
  # machine, and two signposts may share a cluster port anyway (see sameport).
  exname="exposed-$leg"
  case "$(uname -s)" in
    Darwin) exposeport=18082 ;;
    MINGW*|MSYS*|CYGWIN*) exposeport=18083 ;;
    *) exposeport=18081 ;;
  esac
  echo "=== expose: $exname:$exposeport → this runner's :18086 ==="
  if ! helper_bin echo-local; then
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
  if [ "$eo" != "expose-ok-$exname" ]; then
    echo "--- expose FAIL — prober said '${eo:-nothing}' (want expose-ok-$exname)"
    echo "    --- expose session output ---"; tail -12 /tmp/expose.out 2>/dev/null | sed 's/^/    /'
    tail -6 /tmp/expose-probe.err 2>/dev/null | sed 's/^/    /'
    sum "**expose (cluster→local)** ❌ — prober said \`${eo:-nothing}\`"; return 1
  fi
  echo "expose OK — a cluster workload reached this runner's local service by name"; sum "**expose (cluster→local)** ✅"

  # dns honesty for a KILLED name — the gateway-poisoning regression, reproduced
  # the way it bit: a name is served, a WITNESS session resolves it (seeding the
  # resolver's "this exists" verdict), the serving session dies, and the witness
  # asks again. plug used to keep answering "found" from a five-minute cache and
  # mint a fake address for a name that was gone — which, echoed into a Docker
  # Desktop cluster, left a real gateway dialling an address that only existed
  # on the workstation, until someone restarted it.
  #
  # The witness must be ONE LONG-LIVED session asking twice, not two sessions:
  # on Linux every session carries its own resolver cache, so a fresh session
  # would start clean and prove nothing — while on macOS and Windows the shared
  # daemon/service answers, which is exactly the incident's shape. One script,
  # both shapes, each OS through its real resolver.
  #
  # The serving session ends by TTL, never by kill: a kill skips the teardown on
  # Windows, and it is the TEARDOWN (unserve, signpost gone) that opens the gap
  # under test. Verdict by curl exit code — locale-proof where message text is
  # not: 6 = could not resolve (the honest answer), 7/28 = connected or hung on
  # a minted fake (the poison), 0 = still served (the teardown never ran).
  local pname="poison-$leg"
  echo "=== dns honesty for a killed name: $pname must be NXDOMAIN within seconds of its session dying ==="
  "$PLUG" --host "$ip" --port "$port" -s "$pname:9096:18087" \
    "$root/echo-local$ext" -addr 127.0.0.1:18087 -text "alive-$pname" -ttl 20s >/tmp/poison-serve.out 2>&1 &
  local poison_pid=$!
  # The address the CLUSTER resolves while the name is served — the one every
  # caller caches for the 600s TTL Docker's DNS hands out. Compared after a
  # relaunch below: the linger's whole contract is that it does not change.
  presolve() { plug curl -s --max-time 10 "http://chaos:8095/resolve?name=$pname" 2>/dev/null | tr -d '\r' | tail -1; }
  local paddr_before=""
  # 20s of life: the witness session takes 1-4s to arm (a cold Windows service
  # is the slow end), so its first probe lands around t=9-12 with several
  # seconds to spare, and its second around t=31 — the name then dead for
  # ~13s, past the 5s check TTL plus the 5s the OS may repeat our old answer.
  sleep 8
  # Captured in PARALLEL with the witness, not before it: presolve is a full
  # plug session (~5-8s on a cold Windows runner), and running it inline pushed
  # the witness past the serving session's ttl — P1 found nothing and the cell
  # could not conclude. The name is alive for both as long as the capture
  # overlaps the ttl window, which the background start guarantees.
  presolve >/tmp/poison-addr-before 2>/dev/null &
  local presolve_pid=$!
  local wout
  wout="$(perl -e 'alarm 60; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" -c bash -c \
    "p1=\$(curl -s --max-time 8 http://$pname:9096/ || true); echo \"P1=\$p1\"; sleep 22; curl -s --max-time 8 -o /dev/null http://$pname:9096/; echo \"P2-RC=\$?\"" 2>/dev/null | tr -d '\r')"
  wait $poison_pid 2>/dev/null
  wait $presolve_pid 2>/dev/null
  paddr_before="$(tr -d '\r' </tmp/poison-addr-before 2>/dev/null | tail -1)"
  local p1 p2rc nstate
  p1="$(printf '%s' "$wout" | sed -n 's/^P1=//p' | head -1)"
  p2rc="$(printf '%s' "$wout" | sed -n 's/^P2-RC=//p' | head -1)"
  if [ "$p1" != "alive-$pname" ]; then
    echo "--- poison test FAIL — the witness never reached $pname while it was served (P1='${p1:-nothing}') — cannot conclude"
    sum "**dns honesty (killed name)** ❌ — witness never saw it alive"; return 1
  fi
  case "$p2rc" in
    6)
      echo "poison test OK — $pname answered, its session died, and the SAME witness got an honest resolution failure"
      sum "**dns honesty (killed name → NXDOMAIN)** ✅" ;;
    7|52)
      # The LINGER outcome (Swarm/k8s): the signpost outlives the session so the
      # name keeps its address, and a connect is accepted-then-closed (52) or
      # refused (7) INSTANTLY — a stopped service's semantics, benched at 0s.
      # What the fix removed is the hang on a minted fake, and that shows as 28.
      echo "poison test OK — $pname still resolves (lingering for relaunch) and fails fast (curl rc $p2rc)"
      sum "**dns honesty (killed name → fast refusal, address kept)** ✅" ;;
    0)
      echo "--- poison test FAIL — $pname still answers after its session's ttl: the teardown never freed the name"
      sum "**dns honesty (killed name)** ❌ — name still served"; return 1 ;;
    28)
      # A HANG. On its own this is the poisoning signature: an address minted for
      # a name that is gone, with nothing behind it. But on Kubernetes it is also
      # what an HONEST linger looks like - the Service is deliberately kept so a
      # relaunch reuses its ClusterIP, and a Service with no endpoints DROPS the
      # SYN instead of refusing it (socks_run.go:92, transport.go:345). Same exit
      # code, opposite meanings, and the code alone cannot separate them.
      #
      # So ask the cluster the question the exit code cannot answer: does this
      # name still EXIST here? A Service still standing means plug told the truth
      # when it resolved the name. NXDOMAIN from inside means it minted an
      # address for something gone, which is the bug this cell exists to catch.
      #
      # Asked through chaos's own resolver, with the FQDN: chaos runs in its
      # per-leg namespace (k8s.res-agents.yaml) while these names are created by
      # the main agent in `default`, and a BARE name does not cross namespaces -
      # the same trick the resilience cell already uses for its VIP. A Service
      # keeps its ClusterIP whether or not it has endpoints, so resolving is
      # exactly the "does it still exist" question and nothing more.
      #
      # NOT by widening the accepted codes: 28 stays a failure everywhere it
      # cannot be explained, or the detector is off.
      pstate=""
      if [ "$family" = k8s ]; then
        pstate="$(plug curl -s --max-time 10 \
          "http://chaos:8095/resolve?name=$pname.default.svc.cluster.local" 2>/dev/null | tr -d '\r' | tail -1)"
      fi
      case "${pstate:-none}" in
        none|unresolved:*)
          echo "--- poison test FAIL - curl exit 28 on $pname, and the cluster does not resolve it either"
          echo "    (${pstate:-no answer}) - an address was minted for a name that is gone: the gateway-poisoning bug"
          sum "**dns honesty (killed name)** ❌ - hung on a name the cluster no longer knows"; return 1 ;;
        *)
          echo "poison test OK - $pname hung because its Service is still standing (resolves to $pstate);"
          echo "                 plug resolved a name that really does still exist, which is the k8s linger"
          sum "**dns honesty (killed name → k8s linger, name still real)** ✅" ;;
      esac ;;
    *)
      echo "--- poison test FAIL — curl exit $p2rc: hang or fake on a dead name (the gateway-poisoning bug)"
      sum "**dns honesty (killed name)** ❌ — rc \`${p2rc:-none}\` on a dead name"; return 1 ;;
  esac

  # The linger's contract, proven the way the incident bit: relaunch the SAME
  # name and ask the cluster again. Swarm keeps its service VIP by in-place
  # update and k8s its ClusterIP — asserted. A plain-Docker signpost is a new
  # container (its relay target is baked into the entrypoint), so compose is
  # reported, never asserted.
  "$PLUG" --host "$ip" --port "$port" -s "$pname:9096:18087" \
    "$root/echo-local$ext" -addr 127.0.0.1:18087 -text "alive-$pname" -ttl 10s >/tmp/poison-serve2.out 2>&1 &
  local poison2_pid=$!
  sleep 7
  local paddr_after
  paddr_after="$(presolve)"
  wait $poison2_pid 2>/dev/null
  if ! is_addr "${paddr_before:-}" || ! is_addr "${paddr_after:-}"; then
    echo "address across the relaunch: NOT MEASURABLE — '${paddr_before:-nothing}' → '${paddr_after:-nothing}'"
    sum "**name keeps its address across a relaunch** · not measurable on this family"
    return 0
  fi
  case "$family" in
    swarm|k8s)
      if [ "$paddr_before" = "$paddr_after" ]; then
        echo "linger OK — $pname kept $paddr_before across kill and relaunch; a caller's 600s cache stays valid"
        sum "**name keeps its address across a relaunch** ✅ \`$paddr_before\`"
      else
        echo "--- linger FAIL — $pname moved from $paddr_before to $paddr_after across the relaunch"
        sum "**name keeps its address across a relaunch** ❌ — \`$paddr_before\` → \`$paddr_after\`"
        return 1
      fi ;;
    *)
      echo "address across the relaunch: $paddr_before → $paddr_after (plain docker recreates — reported)"
      sum "**name address across a relaunch** · docker recreates (\`$paddr_before\` → \`$paddr_after\`)" ;;
  esac

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
  exname="exposedvar-$leg"
  case "$(uname -s)" in
    Darwin) exposeport=18102 ;;
    MINGW*|MSYS*|CYGWIN*) exposeport=18103 ;;
    *) exposeport=18101 ;;
  esac
  echo "=== exposevar: $exname:$exposeport → a port plug picks, injected as {PORT} ==="
  if ! helper_bin echo-local; then
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
  gwname="gwsink-$leg"
  case "$(uname -s)" in
    Darwin) gwcport=18092 ;;
    MINGW*|MSYS*|CYGWIN*) gwcport=18093 ;;
    *) gwcport=18091 ;;
  esac
  echo "=== gateway callback: external POST → gateway → $gwname → our sink :$gwlocal ==="
  if ! helper_bin sink; then
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
    # Three attempts spanning ~39s all landed inside ONE tailnet outage on the
    # 2.11.0 release run, and the sink's own log recorded
    # "re-provisioned and verified after reconnect" seconds after this cell had
    # already given up. Retrying harder would be guessing at a duration for the
    # third time; ask the session instead. plug says when it is rebuilding the
    # path and when it is done, so judge after it says so — and only then.
    if grep -q "re-arming" /tmp/gw.out 2>/dev/null; then
      echo "    (the session is re-arming after a transport blip — waiting for it to say it is back)" >&2
      for _ in $(seq 1 20); do
        grep -q "re-provisioned and verified" /tmp/gw.out 2>/dev/null && break
        sleep 3
      done
      for _ in 1 2 3; do
        out="$(curl -s --max-time 10 -X POST "http://$ip:18090/call" \
          -H 'content-type: application/json' -d "$body" 2>>/tmp/gw-post.err | tr -d '\r' | tail -1)"
        [ "$out" = "$want" ] && { echo PASS; return; }
        sleep 3
      done
    fi
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
# the agent). The prober is the in-cluster witness: what does
# http://tko-<leg>:<port>/ answer — before, during, after.
do_takeover() {
  local tname tport
  case "$(uname -s)" in
    Darwin)               tname=tko-mac   tport=8086 ;;
    MINGW*|MSYS*|CYGWIN*) tname=tko-win   tport=8087 ;;
    *)                    tname=tko-linux tport=8085 ;;
  esac
  echo "=== takeover: park the deployed $tname, serve ours, restore ==="
  if ! helper_bin echo-local; then
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
# agent-allocated port. Every port must reach ITS OWN local listener from
# inside the cluster — reaching the wrong one is the bug this cell pins down.
do_multiport() {
  local name p1 p2 p3
  name="multip-$leg"
  case "$(uname -s)" in
    Darwin)               p1=18143 p2=18144 p3=18145 ;;
    MINGW*|MSYS*|CYGWIN*) p1=18146 p2=18147 p3=18148 ;;
    *)                    p1=18140 p2=18141 p3=18142 ;;
  esac
  echo "=== multi-port: $name:18131+18132+18133, one process, each port its own backend ==="
  if ! helper_bin echo-local; then
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
  na="samep-a-$leg" nb="samep-b-$leg"
  case "$(uname -s)" in
    Darwin)               pa=18123 pb=18124 ;;
    MINGW*|MSYS*|CYGWIN*) pa=18125 pb=18126 ;;
    *)                    pa=18121 pb=18122 ;;
  esac
  echo "=== same-port: $na:18120 AND $nb:18120, both live, both reachable ==="
  if ! helper_bin echo-local; then
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
  cname="col-$leg"
  case "$(uname -s)" in
    Darwin)               cport=18085 ;;
    MINGW*|MSYS*|CYGWIN*) cport=18086 ;;
    *)                    cport=18084 ;;
  esac
  echo "=== collision: a second -s on $cname (held by a live session) must be refused ==="
  if ! helper_bin echo-local; then
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
  lname="lease-$leg"
  case "$(uname -s)" in
    Darwin)               lport=18131 lloc=18134 ;;
    MINGW*|MSYS*|CYGWIN*) lport=18132 lloc=18135 ;;
    *)                    lport=18130 lloc=18133 ;;
  esac
  echo "=== lease: $lname stays its own session's after the signpost is swept ==="
  if ! helper_bin echo-local; then
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
  if ! helper_bin echo-local; then
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
  # A SECOND name on the same session, this one owned by nobody: no deployed
  # workload, so no parking receipt — which is the condition under which the
  # signpost is reused instead of replaced. $rname cannot answer that question,
  # since taking it over is exactly the case that must still replace it (the
  # receipt is what scales the parked workload back up).
  local vipname="vip-$leg"
  PLUG_KEEPALIVE_SECS=5 "$PLUG" --host "$ip_b" --port "$rsshport" -s "$rname:$rport:18123" -s "$vipname:9099:18123" \
    "$root/echo-local$ext" -addr 127.0.0.1:18123 -text "local-res-$leg" -ttl 150s >/tmp/resilience.out 2>&1 &
  local res_pid=$! during="" after_crash="" after=""
  sleep 8
  for _ in 1 2 3; do during="$(bprobe)"; [ "$during" = "local-res-$leg" ] && break; sleep 3; done

  # The address a workload in the cluster resolves the name to, RIGHT NOW —
  # asked from inside, because that is the address callers cache and keep using.
  # On k8s the chaos service answers in `default` while the per-leg Services live
  # in plug-res-<leg>, and a BARE name does not cross namespaces — the lookup
  # failed identically before and after, which is what made this "NOT
  # MEASURABLE". The FQDN crosses; the other families have one flat space and
  # take the name as-is.
  local vipq="$vipname"
  [ "$family" = k8s ] && vipq="$vipname.plug-res-$leg.svc.cluster.local"
  cresolve() { plug_to "$ip_b" curl -s --max-time 10 "http://chaos:8095/resolve?name=$vipq" 2>/dev/null | tr -d '\r' | tail -1; }
  local addr_before addr_after
  addr_before="$(cresolve)"

  # Before killing the agent: the reconnect with NO death — the transport drops
  # while the session AND the agent keep running (a laptop waking, a VPN
  # switching, a Docker Desktop hiccup). The session re-provisions its name, and
  # the signpost must be REUSED in place or the address moves under callers that
  # were working fine.
  #
  # A restarted agent cannot show this: its boot gc sweeps its own signposts, so
  # the address is legitimately gone and there is nothing left to reuse. Killing
  # only the SSH SESSIONS - listener untouched - is what leaves something.
  # Targeted at THIS LEG's agent, never the shared one: three legs run
  # concurrently. Docker/Swarm only (pod exec needs SPDY the chaos service does
  # not speak), so k8s reports rather than asserts.
  local raddr_before raddr_after
  addr_bad=0
  raddr_before="$(cresolve)"
  plug_to "$ip_b" curl -s --max-time 10 "http://chaos:8095/kill-sessions?svc=$ragent" >/tmp/killsess.out 2>&1 || true
  # Did the kill actually happen? On k8s it answers 501 (pod exec needs SPDY),
  # so NOTHING reconnects — and an unchanged address would then read as proof
  # when it is merely the absence of an event. Say that, rather than let a
  # reader take silence for evidence.
  if ! grep -q killing /tmp/killsess.out 2>/dev/null; then
    echo "live-reconnect: NOT EXERCISED here — $(head -c 90 /tmp/killsess.out 2>/dev/null | tr -d '\r\n')"
    sum "**name keeps its address across a live reconnect** · not exercised on this family"
  else
  sleep 20 # keepalive (5s cadence here) notices, reconnect re-arms and re-provisions
  raddr_after="$(cresolve)"
  if ! is_addr "${raddr_before:-}" || ! is_addr "${raddr_after:-}"; then
    echo "live-reconnect address: NOT MEASURABLE — '${raddr_before:-nothing}' → '${raddr_after:-nothing}'"
    sum "**name keeps its address across a live reconnect** · not measurable on this family"
  else
    case "$family" in
      swarm)
        if [ "$raddr_before" = "$raddr_after" ]; then
          echo "live-reconnect OK — $vipname kept $raddr_before while its agent stayed up (signpost reused in place)"
          sum "**name keeps its address across a live reconnect** ✅ \`$raddr_before\`"
        else
          echo "--- live-reconnect FAIL — $vipname moved from $raddr_before to $raddr_after on a mere transport blip"
          sum "**name keeps its address across a live reconnect** ❌ — \`$raddr_before\` → \`$raddr_after\`"
          addr_bad=1
        fi ;;
      *)
        echo "live-reconnect address: $raddr_before → $raddr_after (reported on this family)"
        sum "**name address across a live reconnect** · \`$raddr_before\` → \`$raddr_after\`" ;;
    esac
  fi
  fi

  # Crash THIS LEG'S agent mid-session (the chaos service answers, then fires).
  plug_to "$ip_b" curl -s --max-time 10 "http://chaos:8095/restart-agent?svc=$ragent" >/dev/null 2>&1 || true
  # keepalive detects (~10-15s at 5s cadence), reconnect re-arms and re-parks;
  # the rebooted agent's boot-gc restored the parked service in between.
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    after_crash="$(bprobe)"
    [ "$after_crash" = "local-res-$leg" ] && break
    sleep 5
  done
  addr_after="$(cresolve)"
  # Whether the name KEEPS its address across this depends on what the backend
  # does at agent boot. This cell RESTARTS the agent, and the boot gc sweeps that
  # agent's own signposts — so on Docker and Swarm there is nothing left to reuse
  # and the replacement gets a new address. Kubernetes keeps its Service, hence
  # its ClusterIP, which is why it alone can be asserted here.
  #
  # The OTHER reconnect — agent alive, transport dropped — is asserted a few
  # lines above via kill-sessions; this one is specifically the post-restart
  # case, where losing the address is correct.
  # An answer that is not an ADDRESS proves nothing, and comparing two identical
  # error strings would read as "kept" — a test that passes without testing,
  # which is worse than one that fails. It happened here: on k8s a BARE name did
  # not cross from chaos (in default) to the per-leg Services (in
  # plug-res-<leg>), so the lookup failed identically before and after — now
  # asked by FQDN. Any reading that is still not an address yields no verdict,
  # said out loud.
  if ! is_addr "$addr_before" || ! is_addr "$addr_after"; then
    echo "address across the agent restart: NOT MEASURABLE — '${addr_before:-nothing}' → '${addr_after:-nothing}'"
    sum "**name address across an agent restart** · not measurable on this family"
  else
    case "$family" in
      k8s)
        if [ "$addr_before" = "$addr_after" ]; then
          echo "address kept across the agent restart — $vipname stayed at $addr_before"
          sum "**name keeps its address across an agent restart** ✅ \`$addr_before\`"
        else
          echo "--- $vipname moved from '$addr_before' to '$addr_after' — the k8s Service should have kept its ClusterIP"
          sum "**name keeps its address across an agent restart** ❌ — \`$addr_before\` → \`$addr_after\`"
          addr_bad=1
        fi ;;
      *)
        echo "address across the agent restart: $addr_before → $addr_after (boot gc sweeps the signpost — expected)"
        sum "**name address across an agent restart** · swept and rebuilt (\`$addr_before\` → \`$addr_after\`)" ;;
    esac
  fi

  wait $res_pid 2>/dev/null # the -ttl fires; teardown restores the deployed service

  # The deployed workload is coming back from a stop, on a runner that has just
  # restarted an agent under it — a "connection reset by peer" here is that
  # container answering mid-restart, not a failure to restore. 15s was enough
  # until it wasn't (macOS, while ubuntu passed the same cell in that run). Same
  # assertion, room to land.
  for _ in $(seq 1 15); do after="$(bprobe)"; [ "$after" = "deployed-res-$leg" ] && break; sleep 3; done

  if [ "$during" = "local-res-$leg" ] && [ "$after_crash" = "local-res-$leg" ] && [ "$after" = "deployed-res-$leg" ] && [ "$addr_bad" = 0 ]; then
    echo "resilience OK — parked, agent restarted, RE-parked (self-heal + boot-gc + re-arm), restored"
    sum "**resilience (agent crash mid-session)** ✅"; return 0
  fi
  echo "--- resilience FAIL — during='$during' after_crash='$after_crash' (want local-res-$leg) after='$after' (want deployed-res-$leg)"
  echo "    --- session output ---"; tail -15 /tmp/resilience.out 2>/dev/null | sed 's/^/    /'
  # The agent it restarted is what restores the parked service through its boot
  # gc, so when the service does not come back the agent is the first suspect —
  # and it can say so itself.
  agent_state "$ip_b" "$ragent"
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
  wait_agent "$ip_b" "$rsshport" || {
    echo "--- update FAIL — $rsshport never came back (the resilience cell crashes this agent)"
    agent_state "$ip_b" "$ragent"
    sum "**plug update** ❌ — per-leg agent never came back"; return 1
  }

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


# update-notify: the BACKGROUND check, end to end — the one thing the update
# cells never covered.
#
# It runs inside the CORE, and the core is whatever the AGENT serves. Against
# prev-agent-* that is the N-1 core, never this branch's — which is why this cell
# was written twice and pulled twice: N-1 either had no check (2.7.3) or had a
# broken one (2.9.0 asked `version` on the tunnel channel, where the verb does
# not exist). Both are now behind us: the fix shipped in 2.9.2, so any N-1 from
# 2.9.3 on carries it. The condition is checked here rather than assumed.
#
# Shape imposed by the design: the check settles for 10s, then dials, asks the
# agent `info` and queries the registry — so the session has to LAST. And its
# verdict is deliberately not announced by the session that found it (that one is
# busy running your command); it is read by the NEXT launch. Two sessions, then.
do_update_notify() {
  local oldport
  case "$(uname -s)" in
    Darwin)               oldport=2227 ;;
    MINGW*|MSYS*|CYGWIN*) oldport=2228 ;;
    *)                    oldport=2226 ;;
  esac
  echo "=== update-notify (cluster B): a session must FIND the newer release, the next one must SAY it ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**update notify** ❌ (cluster B)"; return 1; }

  local prev
  prev="$(ssh -n -p "$oldport" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
          -o LogLevel=ERROR "get@$ip_b" version 2>/dev/null | tr -d '\r')"
  if ! printf '%s' "$prev" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "--- update-notify SKIP — the previous-release agent answered '${prev:-nothing}', not an x.y.z release"
    sum "**update notify** – (no usable N-1)"; return 0
  fi
  # The precondition that sank this cell twice, now verified instead of hoped:
  # the check itself must exist AND work in the core this agent serves.
  case "$prev" in
    2.7.*|2.8.*|2.9.0|2.9.1)
      echo "--- update-notify SKIP — N-1 is $prev, whose background check predates the 05/08 fix (needs >= 2.9.2)"
      sum "**update notify** – (N-1 $prev too old)"; return 0 ;;
  esac
  echo "    N-1 is $prev — its core carries the fixed check"

  if [ ! -x "$root/echo-local$ext" ] && ! helper_bin echo-local; then
    echo "--- update-notify FAIL — echo-local did not build"; sum "**update notify** ❌ (build)"; return 1
  fi


  # Session 1 — long enough for the check to settle, dial, ask `info` and reach
  # the registry. It writes the verdict; it does not announce it.
  # -c: a pure client. Nothing to name and no port to reserve — this cell is
  # about the background check, not about being reachable. (-s or -c is
  # mandatory since 2.0: plug refuses to guess what a process is to the cluster.)
  "$PLUG" --host "$ip_b" --port "$oldport" -c \
    "$root/echo-local$ext" -addr 127.0.0.1:18141 -text "notify-$leg" -ttl 40s >/tmp/notify1.out 2>&1 || true

  # Session 2 — the launcher reads what session 1 recorded, on its way past.
  local out2 rc2=0
  out2="$("$PLUG" --host "$ip_b" --port "$oldport" -c \
          "$root/echo-local$ext" -addr 127.0.0.1:18142 -text "notify2-$leg" -ttl 3s </dev/null 2>&1)" || rc2=$?
  printf '%s\n' "$out2" | sed 's/^/    /'

  # An invocation plug REFUSES prints its usage and exits — which reads as "said
  # nothing about an update" and sent the first version of this cell chasing the
  # check for nothing. Name that case for what it is.
  if printf '%s' "$out2" | grep -q "tell plug what this process is"; then
    echo "--- update-notify FAIL — plug refused the invocation (missing -s/-c), so no session ever ran"
    sum "**update notify** ❌ — invocation refused"; return 1
  fi
  if ! printf '%s' "$out2" | grep -q "update available"; then
    # Before blaming the code: could this machine do what the check does?
    #
    # The check lists the repository's tags FROM HERE and has no fallback — by
    # design ("a timeout there is not a slower path, it is no check at all,
    # silently"). On the GitHub macOS runners Docker Hub times out, so the check
    # correctly finds nothing and there is nothing to announce. The cell cannot
    # tell that apart from a defect unless it tries the same request itself.
    #
    # Tried HERE, after the fact, rather than predicted before: a first version
    # probed /v2/ up front, and one leg sailed past that probe and failed anyway
    # — reachability at one instant does not predict a token exchange plus a tag
    # listing a minute later. Only the real request, at the moment it matters,
    # settles it.
    local tok tags
    tok="$(curl -s --max-time 10 "https://auth.docker.io/token?service=registry.docker.io&scope=repository:softwarity/plug:pull" 2>/dev/null \
           | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
    tags=""
    [ -n "$tok" ] && tags="$(curl -s --max-time 20 -H "Authorization: Bearer $tok" \
                              "https://registry-1.docker.io/v2/softwarity/plug/tags/list?n=1" 2>/dev/null)"
    if ! printf '%s' "$tags" | grep -q '"tags"'; then
      echo "--- update-notify SKIP — this machine cannot list the repository's tags (the same request the check makes, and it has no fallback)"
      sum "**update notify** – (registry unreachable from this runner)"; return 0
    fi
    echo "--- update-notify FAIL — the second launch said nothing about an update, while the agent runs $prev, a newer release is published, AND this machine can reach the registry"
    echo "    (session 1 output follows)"; sed 's/^/    /' /tmp/notify1.out 2>/dev/null | tail -20
    sum "**update notify** ❌ — nothing announced"; return 1
  fi
  # It must name a release NEWER than the one running — announcing the version
  # already deployed would be worse than silence.
  local named
  named="$(printf '%s' "$out2" | sed -n 's/.*update available: v\([0-9][0-9.]*\).*/\1/p' | head -1)"
  if [ -z "$named" ] || [ "$named" = "$prev" ]; then
    echo "--- update-notify FAIL — announced '${named:-nothing}' while running $prev"
    sum "**update notify** ❌ — announced ${named:-nothing}"; return 1
  fi
  echo "update-notify OK — running $prev, a session found $named and the next launch announced it (rc $rc2)"
  sum "**update notify** ✅ — $prev → $named announced by the following launch"
}

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
  wait_agent "$ip_b" "$rsshport" || {
    echo "--- update_tag FAIL — $rsshport never came back (the resilience cell crashes this agent)"
    sum "**update tag** ❌ — per-leg agent never came back"; return 1
  }

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
  updatenotify) do_update_notify ;;
  updatejump)   do_update_jump ;;
  updatetag)    do_update_tag ;;
  *) echo "unknown phase: $phase (want setup|env|matrix|multicluster|outage|expose|exposevar|gateway|takeover|collision|lease|resilience|update)" >&2; exit 2 ;;
esac
