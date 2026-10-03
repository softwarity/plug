#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2034 # what is defined here is read by the cells
# The helpers every cell of the mesh e2e shares. Sourced by
# scripts/ci/e2e-matrix.sh, which has already set:
#
#   phase    the cell being run (the name of the file under scripts/ci/e2e/)
#   peer     cluster A's tailnet name     peer_b   cluster B's
#   port     the agent's ssh port         root     the repository
#
# Nothing here runs a cell: it defines what a cell may call. Portable to macOS's
# bash 3.2 (no associative arrays, no name references), and everything that
# talks to plug is bounded (perl's alarm, wait_bg, --max-time) so a hung child
# fails the cell in seconds instead of the job in half an hour.
clients="$root/e2e/clients"
cd "$root" || exit 1
envfile="${RUNNER_TEMP:-/tmp}/plug-e2e-env" # shared state, survives across steps

# All four languages, on every family. The override exists for a bench run, not
# for CI - and there is a reason worth keeping written down.
#
# It was briefly used to cut swarm and k8s down to Go, on the argument that the
# LANGUAGE axis tests the client (four resolvers: the JVM's cache, c-ares in
# Node, libc in Python, Go's own) while the PROTOCOL axis tests the family's
# network - so the two would be orthogonal and the compose legs could carry the
# languages alone. That argument is WRONG, and cost 276s on one leg to be so.
#
# Each language does not merely resolve differently: it brings its OWN
# IMPLEMENTATION of the wire protocol. AMQP is amqp091-go, amqplib, pika and
# com.rabbitmq - four codebases with different framing, heartbeats, pooling and
# write sizes. That traffic crosses the tunnel and then the family's network,
# where a VXLAN overlay's 1450-byte MTU does not answer a driver writing in
# large blocks the way it answers one writing small. Language and family are not
# independent, and nobody had measured that they were.
LANGS="${E2E_LANGS:-go node python java}"
# proto:host:port - the by-name target for each service.
PROTOS="http:httpbin:8080 postgres:postgres:5432 redis:redis:6379 mongo:mongo:27017 amqp:rabbitmq:5672 mqtt:mosquitto:1883 grpc:grpc:50051 websocket:wsserver:8090"

# Which cluster family this run is against, read off the cluster name the
# workflow passes (plug-compose-… / plug-swarm-… / plug-k8s-…, see _cluster.yml).
# Every cell runs on all three; a couple of them assert something only one
# backend can give, and say so rather than skipping.
case "$peer_b" in
  *-swarm-*) family=swarm ;;
  *-k8s-*)   family=k8s ;;
  *)         family=compose ;;
esac

# ============================ the OS table, once ============================
#
# Two names for the machine this runs on, and they are not the same thing.
#
#   os   which fixtures the CLUSTER declares for it: flaky-<os>, tko-<os>,
#        res-tko-<os>, res-agent-<os>, prev-agent-<os>, vol-<os>, and the
#        cluster ports those listen on. linux, mac or win. The arm64 leg is
#        Linux here: it shares the Linux fixtures (the one it takes over, tko,
#        has an -arm twin so two legs never park the same service).
#   leg  what THIS leg creates through -s (run-<leg>, exposed-<leg>, col-<leg>,
#        ...): the same three plus linuxarm, because two Linux legs (amd64 and
#        arm64) share cluster A and claiming one name twice is a collision,
#        which is another cell's subject entirely.
#
# Everything per-OS is decided HERE, once, and the cells read variables. The
# cells used to carry twenty-eight `case "$(uname -s)"` blocks of their own, and
# the one that keyed on OS alone where it needed the leg cost the publication
# run of 2.16.1 (two Linux legs took tko-linux within the same minute).
case "$(uname -s)" in
  Darwin)               os=mac ;;
  MINGW*|MSYS*|CYGWIN*) os=win ;;
  *)                    os=linux ;;
esac
arch=amd64; case "$(uname -m)" in aarch64|arm64) arch=arm64 ;; esac
leg="$os"; [ "$os$arch" = linuxarm64 ] && leg=linuxarm

ext=""; py="python3"
[ "$os" = win ] && { ext=".exe"; py="python"; }
# The prebuilt bundle's target tag (see take_prebuilt below).
case "$os" in mac) gtag=darwin ;; win) gtag=windows ;; *) gtag=linux ;; esac
gtag="$gtag-$arch"

# Per OS: the cluster ports the fixtures listen on, and the LOCAL ports this
# machine's cells bind. Local ports are per OS because they are local; two legs
# are two machines. Cluster-side names and the -s cluster ports stay per OS too,
# because two signposts may share a cluster port (that is what sameport proves)
# and the arm64 leg derives its NAMES from $leg.
case "$os" in
  mac)
    flaky_name=flaky-mac      tko_name=tko-mac    tko_port=8086
    vol_name=vol-mac          vol_port=8121
    res_name=res-tko-mac      res_port=8116       res_agent=res-agent-mac   res_sshport=2224
    prev_port=2227
    expose_port=18082         exposevar_port=18102  gw_cport=18092        col_port=18085
    lease_port=18131          lease_local=18134
    sp_pa=18123 sp_pb=18124   mp_p1=18143 mp_p2=18144 mp_p3=18145 ;;
  win)
    flaky_name=flaky-win      tko_name=tko-win    tko_port=8087
    vol_name=vol-win          vol_port=8122
    res_name=res-tko-win      res_port=8117       res_agent=res-agent-win   res_sshport=2225
    prev_port=2228
    expose_port=18083         exposevar_port=18103  gw_cport=18093        col_port=18086
    lease_port=18132          lease_local=18135
    sp_pa=18125 sp_pb=18126   mp_p1=18146 mp_p2=18147 mp_p3=18148 ;;
  *)
    flaky_name=flaky-linux    tko_name=tko-linux  tko_port=8085
    vol_name=vol-linux        vol_port=8120
    res_name=res-tko-linux    res_port=8115       res_agent=res-agent-linux res_sshport=2223
    prev_port=2226
    expose_port=18081         exposevar_port=18101  gw_cport=18091        col_port=18084
    lease_port=18130          lease_local=18133
    sp_pa=18121 sp_pb=18122   mp_p1=18140 mp_p2=18141 mp_p3=18142 ;;
esac

# -s is mandatory: every `plug <cmd>` names itself in the cluster. The UPWARD
# families serve nothing, so they publish a throwaway name - ONE per leg (the
# legs share cluster A's agent and a remote-forward port binds once on it;
# the helper force-replaces a same-named signpost, so reuse is safe). The local
# port is a dummy: the startup self-test loops the nonce back inside the session.
#
# tko_fwd is the agent port the workload-env cell's takeovers bind: per LEG,
# like the name, since the arm64 and amd64 Linux legs run concurrently on one
# compose cluster and a shared port queued the second takeover behind the
# first until its alarm killed it.
case "$leg" in
  mac)      sport=18072 tko_fwd=18099 ;;
  win)      sport=18073 tko_fwd=18099 ;;
  linuxarm) sport=18074 tko_fwd=18097 tko_name=tko-arm tko_port=8088 ;;
  *)        sport=18071 tko_fwd=18099 ;;
esac
serve="-s run-${leg}:${sport}:9"   # hyphen only - an underscore is not a valid DNS label
# A free block of cluster ports, four per leg: the cells use up to 18148, and
# the legs are spaced so two of them never overlap (linux 18150, mac 18154,
# win 18158, arm 18162).
mport_base=$(( 18150 + (sport - 18071) * 4 ))

# ConnectTimeout, or wait_cluster's "200 x 3s" is not ten minutes: a tailnet
# peer that is known but not answering lets the TCP connect sit in SYN for the
# OS's own timeout (over a minute on Windows), one attempt at a time.
SSH_OPTS="-p $port -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes -o ConnectTimeout=5"
# The same options for an agent on ANOTHER port (the per-leg agents).
ssh_agent() { # ssh_agent <ip> <port> <verb...>
  local sa_ip="$1" sa_port="$2"; shift 2
  ssh -n -p "$sa_port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o LogLevel=ERROR -o BatchMode=yes -o ConnectTimeout=5 "get@$sa_ip" "$@"
}

# --- prebuilt clients (see scripts/ci/build-clients.sh) -----------------------
# PREBUILT_CLIENTS points at what one Linux runner produced for every leg: the
# Go client and helpers cross-compiled per target, the jar, and node_modules.
# Building those on each leg cost 493s of a 769s setup on Windows, three times
# over, for bytes that are identical between legs.
#
# Unset - a local run, a manual dispatch - and everything builds as it always
# did. That fallback is not decoration: it is how anyone runs this script
# outside CI, so it stays exercised by the bench.
prebuilt="${PREBUILT_CLIENTS:-}"

# take_prebuilt <name> <destination> - install a cross-compiled binary if it is
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

# helper_bin <name> - put e2e/<name>'s binary at $root/<name>$ext. echo-local is
# rebuilt by NINE cells and sink by one; none of them needs its own compiler.
helper_bin() {
  take_prebuilt "$1" "$root/$1$ext" && return 0
  ( cd "$root/e2e/$1" && go build -o "$root/$1$ext" . )
}

# --- wait for a cluster over the tailnet (echoes its IP once its agent answers) ---
# 200×3s ≈ 10min: the cluster run boots its runner, joins the mesh and PULLS the
# image ci.yml publishes for this commit (built in parallel, so the pull may
# wait on the build) - and the k8s family adds a kind create + image loads + a
# rollout on top.
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
# a reason that has nothing to do with what they assert - a cascade seen once on
# a loaded macOS runner, where ubuntu passed the identical cells.
wait_agent() {
  wa_ip="$1"; wa_port="$2"
  for _ in $(seq 1 40); do
    if ssh_agent "$wa_ip" "$wa_port" version >/dev/null 2>&1; then
      return 0
    fi
    sleep 3
  done
  return 1
}

# agent_state prints WHY an agent is not answering - its container state and its
# own last lines, asked from inside the cluster through the chaos service.
#
# Written after three red cells pointed at one invisible cause: the resilience
# cell restarts an agent by design, and when it did not come back every cell
# using it failed saying only that. Two rounds of guessing at timeouts followed.
# A cell that cannot explain its failure sends whoever reads it looking in the
# wrong place - that is what this exists to stop.
agent_state() {
  as_ip="$1"; as_svc="$2"
  echo "    --- state of $as_svc, from inside the cluster ---"
  plug_to "$as_ip" curl -s --max-time 15 "http://chaos:8095/agent-state?svc=$as_svc" 2>/dev/null \
    | tr -d '\r' | sed 's/^/    /' | tail -35
}

# Per-cell timeout: a client with no timeout of its own must not hang the job.
# perl's alarm is on every runner (incl. Git Bash) and survives exec.
plug_to() { to="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$to" --port "$port" $serve "$@"; }
plug()    { plug_to "$ip" "$@"; }
# plug_serving <-s name:cport:lport> <cmd…> - plug() with a name of OUR choosing
# instead of the leg's single one, so several sessions can run at once. Claiming
# `$serve` twice at the same moment is a collision, which is another cell's
# subject entirely.
plug_serving() { pv="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$ip" --port "$port" $pv "$@"; }
# plug_bounded <seconds> <plug args...> - a plug invocation under ITS OWN
# alarm and nothing else (no -s, no host): the caller says everything. Every
# plug call in the harness goes through an alarm, because the cell watchdog is
# the last resort and not the budget.
plug_bounded() { pb_s="$1"; shift; perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' "$pb_s" "$PLUG" "$@"; }
# prober_fetch <url> [ip] - what a workload INSIDE the cluster gets for <url>,
# asked through the prober service (cluster A unless an ip says otherwise).
# The in-cluster witness every takeover-shaped cell reads: before, during, after.
prober_fetch() {
  plug_to "${2:-$ip}" curl -s --max-time 10 "http://prober:8097/fetch?url=$1" 2>/dev/null | tr -d '\r' | tail -1
}
cmd_go()     { echo "$clients/go/eclient$ext"; }
cmd_node()   { echo "node $clients/node/client.js"; }
cmd_python() { echo "$py $clients/python/client.py"; }
cmd_java()   { echo "java -jar $clients/java/target/client.jar"; }

# retry_until <tries> <delay> <want> <probe-fn> [args...]
#
# <probe-fn> up to <tries> times, <delay> seconds after each miss (the last one
# included, exactly as the loops it replaces slept), until an answer EQUALS
# <want>. The last answer is left in $ru_out for the caller's message; returns
# 0 on a match. Twenty-one hand-rolled `for _ in 1 2 3` loops said this, each
# with its own sleep and its own variable.
retry_until() {
  local ru_tries="$1" ru_delay="$2" ru_want="$3"; shift 3
  local ru_i=0
  ru_out=""
  while [ "$ru_i" -lt "$ru_tries" ]; do
    ru_out="$("$@")"
    [ "$ru_out" = "$ru_want" ] && return 0
    sleep "$ru_delay"
    ru_i=$((ru_i + 1))
  done
  return 1
}

# is_addr: the reading is an IPv4 address, not an error string. Every address
# assertion goes through it, and comparing two identical error messages once read
# as "address kept", a test that passed without testing.
#
# The first version only asked whether the string was made of digits and dots,
# which "....", "999" and "1" all are. Two truncated replies from a probe that
# had stopped answering properly would have compared equal again, in a different
# costume. Four numbers, each in range, or it is not an address.
is_addr() {
  case "${1:-}" in
    *[!0-9.]*|"") return 1 ;;
  esac
  local IFS=. n=0 part
  for part in $1; do
    n=$((n + 1))
    case "$part" in
      ""|*[!0-9]*) return 1 ;;
    esac
    [ "${#part}" -gt 3 ] && return 1
    [ "$part" -gt 255 ] && return 1
  done
  [ "$n" -eq 4 ]
}
glyph() { case "$1" in PASS) printf "✅" ;; FAIL) printf "❌" ;; SKIP) printf "·" ;; *) printf "?" ;; esac; }
sum()   { echo "$*" >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"; }

# skip_cell <label> <reason>: a cell that could not measure what it is named
# for, for a reason that belongs to the RUNNER and not to plug (a registry this
# machine cannot reach). It exits 0, because a red for the runner's network
# would be a lie in the other direction, but it must never READ as a pass:
# update-notify returned 0 on "registry unreachable", which is the usual case
# on the macOS runners, and the summary showed nothing that told the two apart.
# So every skip is counted, annotated on the job (a ::warning:: survives the
# step going green) and written to the summary as a SKIP with its number;
# leg_tally closes the leg with the total.
skip_file="${RUNNER_TEMP:-/tmp}/plug-e2e-skips"
skip_cell() {
  printf '%s: %s\n' "$1" "$2" >> "$skip_file"
  sk_n="$(wc -l < "$skip_file" | tr -d ' ')"
  echo "--- $1 SKIP (#$sk_n on this leg, not a pass) - $2"
  echo "::warning::$phase: '$1' was SKIPPED, not passed: $2"
  sum "**$1** $(glyph SKIP) SKIP #$sk_n - $2"
}
leg_tally() {
  [ -s "$skip_file" ] || return 0
  sum "**skipped on this leg: $(wc -l < "$skip_file" | tr -d ' ')** (counted, not passed)"
  sed 's/^/- /' "$skip_file" >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"
}

# assert_all <probe-fn> <want> <n> <answers> [deadline-epoch] [min]
#
# EVERY answer, not the first one that fits. Up to <n> reads of <probe-fn>,
# each of which must be <want>. The loop it replaces stopped at the first
# matching answer, and a takeover that handed HALF the requests to the deployed
# pod (a Kubernetes Service whose old EndpointSlice survived the park, from
# 2.12.0 to 2.20.0) passed it every time for four weeks. The same shape is the
# orphan cell's whole subject (a restore that left the dead session's route in)
# and what sameport and multiport exist to refuse (a port answering the wrong
# backend). One probe, one rule, so a cell cannot quietly keep the weak form.
#
# <answers> lists the PREFIXES a real answer starts with ("deployed- local-").
# A read matching none of them is not a verdict but a blink: an empty body, or
# the connection error the prober relays while the controllers are still
# swapping endpoints after a restore, when for some hundreds of ms the Service
# has no endpoint at all. That read is taken again once, a second later. What
# fails is a REAL answer that is not <want>, or a blink that stays.
#
# <deadline-epoch> stops the reads while the session under test is certainly
# alive: a read costs up to 8s on a Windows runner, and one taken after the
# session's own -ttl says "deployed", which looks exactly like a split route
# and is not one. <min> (default <n>) is how many reads must have fitted.
#
# Results, for the caller's message: aa_seq stamps every answer with its second
# (from $aa_clock when the caller set one, else from the first read), aa_why
# says in one line what went wrong or is empty, aa_reads/aa_ok/aa_bad count.
assert_all() {
  local aa_probe="$1" aa_want="$2" aa_n="$3" aa_answers="$4" aa_deadline="${5:-}" aa_min="${6:-$3}"
  local aa_r aa_p aa_real aa_t0 aa_stray=""
  aa_t0="${aa_clock:-$(date +%s)}"
  aa_seq=""; aa_ok=0; aa_bad=0; aa_reads=0; aa_why=""
  while [ "$aa_reads" -lt "$aa_n" ]; do
    if [ -n "$aa_deadline" ] && [ "$(date +%s)" -ge "$aa_deadline" ]; then break; fi
    aa_r="$("$aa_probe")"
    aa_real=""
    for aa_p in $aa_answers; do case "$aa_r" in "$aa_p"*) aa_real=1 ;; esac; done
    if [ -z "$aa_real" ]; then sleep 1; aa_r="$("$aa_probe")"; fi
    aa_reads=$((aa_reads + 1))
    aa_seq="$aa_seq $(( $(date +%s) - aa_t0 ))s:${aa_r:-nothing}"
    if [ "$aa_r" = "$aa_want" ]; then aa_ok=$((aa_ok + 1)); else aa_bad=$((aa_bad + 1)); aa_stray="$aa_r"; fi
  done
  if [ "$aa_bad" -gt 0 ]; then
    aa_why="$aa_ok/$aa_reads $aa_want, $aa_bad '${aa_stray:-nothing}' (by second:$aa_seq)"
  elif [ "$aa_reads" -lt "$aa_min" ]; then
    aa_why="only $aa_reads read(s) fitted before the deadline (by second:$aa_seq)"
  fi
  [ -z "$aa_why" ]
}

# How this phase left, printed whatever happens. A cell that prints its OK and
# then dies takes its cause with it: `exposevar` did exactly that, twice, ending
# on 143 (SIGTERM) with nothing after its success line and no idea what signalled
# the shell. A status above 128 is a signal, and naming it is the difference
# between a mystery and a lead.
trap 'rc=$?
  [ -n "${cell_watchdog:-}" ] && kill "$cell_watchdog" 2>/dev/null
  if [ "$rc" -gt 128 ]; then
    echo "e2e-matrix: phase $phase left on signal $((rc - 128)) (exit $rc): the SHELL was signalled, not just a child" >&2
  elif [ "$rc" -ne 0 ]; then
    echo "e2e-matrix: phase $phase left with exit $rc" >&2
  fi' EXIT

# Every process on the machine, and the two words that matter are `-A` and the
# fallback, both of them measured rather than assumed.
#
# WITHOUT -A, ps lists only the processes attached to the caller's terminal, and
# a CI step has no terminal: asked from inside the watchdog, plain ps returned
# NOTHING. So the diagnosis it printed was a heading with an empty list under
# it, on every hang it ever caught. And Git Bash's ps accepts no -o at all,
# printing its own columns (PID PPID PGID WINPID TTY UID STIME COMMAND) whose
# first two fields are the ones read here, which is what the fallback is for.
ps_all() { ps -A -o pid=,ppid=,etime=,args= 2>/dev/null || ps 2>/dev/null; }

# stop_bg ends a backgrounded session and SAYS what it found, because the two
# expose cells were killing a raw PID and discarding everything about it.
#
# On Windows this matters more than it looks: PIDs are recycled aggressively, so
# a `kill $pid` on a process that already exited can reach something else
# entirely. If that ever happens the numbers printed here are what tells us -
# the shell's own pid is printed beside the target for exactly that comparison.
# wait_bg waits for a backgrounded session to end, and NEVER forever.
#
# Every wait on a plug session in this harness was unbounded, and that is how two
# Windows legs burned 27 and 28 minutes to their timeout in a row, on two
# different cells, with no output at all: the session did not exit, `wait` sat on
# it, and the runner killed the job. The cells before it had passed and the ones
# after never ran, so the log named nothing.
#
# Bounded, it becomes a failure with a sentence instead of a job that vanishes.
# The hard kill is deliberate: a session that ignored a TERM is not going to
# answer a second one, and the leg still has a chain of cells to run.
wait_bg() {
  wb_pid="$1" wb_what="$2" wb_max="${3:-60}" wb_n=0
  while kill -0 "$wb_pid" 2>/dev/null && [ "$wb_n" -lt "$wb_max" ]; do
    sleep 1
    wb_n=$((wb_n + 1))
  done
  if kill -0 "$wb_pid" 2>/dev/null; then
    echo "--- $wb_what (pid $wb_pid) did not exit after ${wb_max}s: killing it hard and going on"
    kill -9 "$wb_pid" 2>/dev/null
    wb_stuck=1
  else
    wb_stuck=0
  fi
  wait "$wb_pid" 2>/dev/null || true
}

stop_bg() {
  sb_pid="$1" sb_what="$2"
  if kill -0 "$sb_pid" 2>/dev/null; then
    sb_alive=yes
  else
    sb_alive=no
  fi
  kill "$sb_pid" 2>/dev/null
  # `|| sb_rc=$?` and not a bare `wait`: a child killed by SIGTERM makes wait
  # return 143, and under `set -e` (which the runner's shell sets) a bare wait
  # then ENDS THE CELL, with its exit status - 143 - reported as the step's.
  # Reproduced locally the moment this helper was written. Both expose cells had
  # that exact shape, so tearing down a session that was still alive could kill
  # the cell that tore it down, and the failure would name a signal nobody sent.
  sb_rc=0
  wait_bg "$sb_pid" "$sb_what" 60
  echo "stop_bg: $sb_what pid=$sb_pid alive-before-kill=$sb_alive stuck=$wb_stuck (this shell is $$)"
}
