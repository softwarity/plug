#!/usr/bin/env bash
# The soak: the one thing the e2e never proves, a session that LASTS.
#
# Every cell of e2e-matrix.sh finishes in seconds, and what plug actually does at
# a user's desk is hold a session all day. The failures that shape belongs to are
# invisible to a short test by construction: memory that creeps, descriptors that
# leak a few per reconnection, a fake-IP pool that fills because entries are
# never recycled. None of them fail a two-minute cell; all of them ruin an
# afternoon.
#
# So this runs weekly, on its own schedule, against the PUBLISHED image rather
# than the branch: the question it answers is "does the release people install
# hold up for hours", which is not the same question the per-push CI asks.
#
# It stands its own cluster on the runner's docker daemon and installs plug FROM
# it, the real one-liner, so the binary under test is the one a user gets.
#
# CI only, and the guard is the same as the keymount cell's: an agent sweeps
# orphaned plug signposts on the daemon it is handed, at boot. A runner's daemon
# is empty and disposable; a workstation's may be a Swarm manager carrying a real
# deployment. To run this on a developer machine, give the agent a
# docker-in-docker daemon instead.
set -uo pipefail

SOAK_MINUTES="${SOAK_MINUTES:-240}"
SOAK_SAMPLE_SECS="${SOAK_SAMPLE_SECS:-60}"
SOAK_IMAGE="${SOAK_IMAGE:-softwarity/plug:latest}"
net=plug-soak
work="${RUNNER_TEMP:-/tmp}/soak"
samples="$work/samples.tsv"

say() { echo "$@"; }
sum() { echo "$*" >> "${GITHUB_STEP_SUMMARY:-/dev/stderr}"; }

# Every process, no header: ps without -A lists only the caller's terminal, and a
# CI step has none. Same reason as the cell watchdog in e2e-matrix.sh.
ps_all() { ps -A -o pid=,ppid= 2>/dev/null; }

# The whole session, not just the launcher. plug is a launcher that runs a core
# that runs your command, so a number read off one of the three is a third of the
# answer - and the leak, wherever it would be, is in the datapath the core holds.
tree_pids() {
  tp_p="$1"; echo "$tp_p"
  for tp_c in $(ps_all | awk -v p="$tp_p" '$2==p {print $1}'); do tree_pids "$tp_c"; done
}

# Resident set of the whole tree, in kB, and open descriptors of the whole tree.
# /proc rather than ps for the descriptors: ps cannot count them, and this runs
# on Linux only.
tree_rss()  { for p in $(tree_pids "$1"); do awk '/^VmRSS:/{print $2}' "/proc/$p/status" 2>/dev/null; done | awk '{s+=$1} END{print s+0}'; }
# sudo, because a process that took file capabilities has /proc/<pid>/fd owned by
# root: an unprivileged ls returns nothing and the count silently reads as zero,
# which is how the first run reported five descriptors falling to two on a live
# session. Runners have passwordless sudo; without it this degrades to the same
# zero rather than failing, and the count is reported, never asserted alone.
tree_fds()  { for p in $(tree_pids "$1"); do sudo ls "/proc/$p/fd" 2>/dev/null | wc -l; done | awk '{s+=$1} END{print s+0}'; }
tree_thr()  { for p in $(tree_pids "$1"); do awk '/^Threads:/{print $2}' "/proc/$p/status" 2>/dev/null; done | awk '{s+=$1} END{print s+0}'; }

# --- the verdicts, pure so they can be proven on synthetic series -------------
#
# A trend, never a threshold: how much memory plug uses is not this script's
# business, whether that number keeps climbing after hours is. The comparison is
# median of the second half against median of the first, which ignores the warm
# up and the odd spike alike.
median() { sort -n | awk '{v[NR]=$1} END{ if(NR==0){print 0} else if(NR%2){print v[(NR+1)/2]} else {print int((v[NR/2]+v[NR/2+1])/2)} }'; }

# Three numbers, and the shape of the rule was wrong before its own selftest
# corrected it. The first version asked for a large PERCENTAGE and a large
# ABSOLUTE growth together, which reads well and lets the worst case through: a
# session already sitting at 400 MB that puts on 60 MB in four hours is up only
# 15%, passes the percentage test, and is exactly the leak this exists to catch.
#
# So: below the NOISE floor nothing is a finding, whatever the ratio - 12 MB to
# 16 MB is a session doing its job. Above it, either a large ratio or a large
# absolute growth is enough on its own.
verdict_growth() { # <a> <b> <pct> <noise> <hard> -> "OK|GROWS|NODATA <pct> <delta>"
  vg_a="$1" vg_b="$2" vg_pct="$3" vg_noise="$4" vg_hard="$5"
  [ "$vg_a" -le 0 ] && { echo "NODATA 0 0"; return; }
  vg_d=$((vg_b - vg_a))
  vg_p=$(( vg_d * 100 / vg_a ))
  if [ "$vg_d" -le "$vg_noise" ]; then echo "OK $vg_p $vg_d"
  elif [ "$vg_p" -gt "$vg_pct" ] || [ "$vg_d" -gt "$vg_hard" ]; then echo "GROWS $vg_p $vg_d"
  else echo "OK $vg_p $vg_d"; fi
}

if [ "${SOAK_SELFTEST:-}" = 1 ]; then
  # The assertions have to be proven before the four hours, not after: a soak
  # whose verdict is wrong costs a week to find out.
  fail=0
  t() { got="$(verdict_growth "$2" "$3" "$4" "$5" "$6")"; case "$got" in $7*) echo "  ok   $1";; *) echo "  FAIL $1: got '$got', want '$7…'"; fail=1;; esac; }
  echo "--- verdicts, on synthetic series ---"
  t "flat session passes"                  100000 101000 20 20000 51200 OK
  t "a real leak is caught"                100000 160000 20 20000 51200 GROWS
  t "a small session that doubles in MB"    12000  16000 20 20000 51200 OK
  t "a large session creeping 60MB"        400000 460000 20 20000 51200 GROWS
  t "growth under the noise floor is not a finding" 400000 415000 20 20000 51200 OK
  t "descriptors: +40 on a base of 30"         30     70  20     8    64 GROWS
  t "descriptors: +6 is noise"                 30     36  20     8    64 OK
  t "no samples says so"                        0  50000 20 20000 51200 NODATA
  echo "--- median ---"
  m="$(printf '5\n1\n3\n' | median)"; [ "$m" = 3 ] && echo "  ok   odd count" || { echo "  FAIL odd: $m"; fail=1; }
  m="$(printf '4\n1\n3\n2\n' | median)"; [ "$m" = 2 ] && echo "  ok   even count" || { echo "  FAIL even: $m"; fail=1; }
  exit "$fail"
fi

case "$(uname -s)" in Linux) ;; *) say "soak is Linux only"; exit 0 ;; esac
if [ -z "${GITHUB_ACTIONS:-}" ]; then
  say "not a CI runner: this stands an agent on THIS machine's docker daemon, and an agent"
  say "sweeps plug signposts at boot. Give it a dind daemon instead, or leave it alone."
  exit 0
fi

cleanup() {
  [ -n "${plug_pid:-}" ] && kill -9 "$plug_pid" 2>/dev/null
  docker rm -f plug-soak-agent plug-soak-httpbin >/dev/null 2>&1
  docker network rm "$net" >/dev/null 2>&1
}
trap cleanup EXIT

mkdir -p "$work"
say "=== a cluster of its own, from $SOAK_IMAGE ==="
docker network create "$net" >/dev/null 2>&1
docker run -d --name plug-soak-agent --network "$net" \
  -v /var/run/docker.sock:/var/run/docker.sock "$SOAK_IMAGE" >/dev/null || { say "the agent would not start"; exit 1; }
docker run -d --name plug-soak-httpbin --network "$net" --network-alias httpbin \
  mccutchen/go-httpbin:v2.15.0 >/dev/null || { say "httpbin would not start"; exit 1; }
up=""
n=0; while [ "$n" -lt 30 ]; do
  docker logs plug-soak-agent 2>&1 | grep -q "ready (v" && { up=1; break; }
  n=$((n + 1)); sleep 1
done
[ -n "$up" ] || { say "the agent never came up:"; docker logs plug-soak-agent 2>&1 | tail -10; exit 1; }
ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' plug-soak-agent)"
say "agent at $ip:22, $(docker logs plug-soak-agent 2>&1 | grep -o 'ready (v[^)]*)' | head -1)"

say "=== install plug from that cluster, the real one-liner ==="
ssh -p 22 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -o BatchMode=yes "get@$ip" install </dev/null | sh || { say "install failed"; exit 1; }
PLUG=""
for c in "$(command -v plug 2>/dev/null || true)" "$HOME/.local/bin/plug" /usr/local/bin/plug; do
  [ -n "$c" ] && [ -x "$c" ] && PLUG="$c" && break
done
[ -n "$PLUG" ] || { say "plug not found after install"; exit 1; }
say "installed: $PLUG ($("$PLUG" version 2>&1 | head -1))"

say "=== a session held for ${SOAK_MINUTES} min, with traffic ==="
: > "$work/traffic.log"
# The traffic is the point: a session that sits idle for four hours proves that
# an idle session survives, which nobody doubted. Each round resolves the name
# again and opens a fresh connection, which is what exercises the fake-IP table
# and the per-connection ownership check.
#
# The two streams go to two files ON PURPOSE. curl writes the status code to
# stdout and plug writes its own notices to stderr, and folding them into one
# file made every notice plug printed - an update available, a re-provision after
# a reconnect - count as a failed round. The traffic log must hold nothing but
# what a round produced.
"$PLUG" --host "$ip" --port 22 -c sh -c '
  while :; do
    if curl -sS --max-time 20 -o /dev/null -w "%{http_code}\n" http://httpbin:8080/get; then :; else echo "ERR"; fi
    sleep 2
  done' > "$work/traffic.log" 2> "$work/plug.log" &
plug_pid=$!
sleep 20
kill -0 "$plug_pid" 2>/dev/null || { say "the session died in its first 20s:"; tail -20 "$work/plug.log" "$work/traffic.log"; exit 1; }
# Alive is not the same as working, and the first run proved the difference: the
# process lived its full twelve minutes and ran not one round, so the soak
# measured an idle launcher and called it healthy. Ask for the first round here,
# where the answer costs 20 seconds instead of the whole run.
if [ ! -s "$work/traffic.log" ]; then
  say "--- FAIL: the session is up but has produced NO traffic in 20s."
  say "    plug said:"; sed 's/^/      /' "$work/plug.log" | tail -20
  say "    the command was: sh -c 'curl ... http://httpbin:8080/get' under plug -c"
  sum "**soak** ❌ (no traffic at all)"
  exit 1
fi
say "first rounds in: $(head -3 "$work/traffic.log" | tr '\n' ' ')"

printf 'elapsed_s\trss_kb\tfds\tthreads\trounds\terrors\n' > "$samples"
deadline=$(( $(date +%s) + SOAK_MINUTES * 60 ))
start=$(date +%s)
while [ "$(date +%s)" -lt "$deadline" ]; do
  now=$(date +%s)
  # No `|| echo 0` here: grep -c already PRINTS 0 when it matches nothing, and
  # only its exit status says so. The fallback appended a second zero, the
  # variable held "0\n0", and `[ "$errors" -gt 0 ]` died on it with "integer
  # expression expected" - a failed assertion that left the verdict green.
  rounds=$(grep -c '^200$' "$work/traffic.log" 2>/dev/null); : "${rounds:=0}"
  errors=$(grep -cvE '^200$|^$' "$work/traffic.log" 2>/dev/null); : "${errors:=0}"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$((now - start))" "$(tree_rss "$plug_pid")" \
    "$(tree_fds "$plug_pid")" "$(tree_thr "$plug_pid")" "$rounds" "$errors" >> "$samples"
  kill -0 "$plug_pid" 2>/dev/null || { say "the session DIED after $(( (now - start) / 60 )) min:"; tail -20 "$work/plug.log" "$work/traffic.log"; break; }
  sleep "$SOAK_SAMPLE_SECS"
done

say "=== what it did ==="
rows=$(( $(wc -l < "$samples") - 1 ))
[ "$rows" -ge 4 ] || { say "only $rows samples, too few to judge a trend"; sum "**soak** ❌ (only $rows samples)"; exit 1; }
half=$(( rows / 2 ))
col() { tail -n +2 "$samples" | cut -f"$1"; }
rss_a=$(col 2 | head -n "$half" | median); rss_b=$(col 2 | tail -n "$half" | median)
fd_a=$(col 3 | head -n "$half" | median);  fd_b=$(col 3 | tail -n "$half" | median)
th_a=$(col 4 | head -n "$half" | median);  th_b=$(col 4 | tail -n "$half" | median)
rounds=$(col 5 | tail -1); errors=$(col 6 | tail -1)
recon=$(grep -ci "reconnect" "$work/plug.log" 2>/dev/null); : "${recon:=0}"

# Memory in kB: 20 MB of noise, and 50 MB of growth is a finding whatever the
# ratio. Descriptors and threads are counted things, so their floors are small
# and their hard limits are counts too: 64 descriptors or 32 threads gained over
# a run is a leak on any base.
v_rss="$(verdict_growth "$rss_a" "$rss_b" 20 20000 51200)"
v_fd="$(verdict_growth  "$fd_a"  "$fd_b"  20     8    64)"
v_th="$(verdict_growth  "$th_a"  "$th_b"  20     8    32)"

sum "### Soak: ${SOAK_MINUTES} min on \`$SOAK_IMAGE\`"
sum ""
sum "| what | first half | second half | verdict |"
sum "|---|---|---|---|"
sum "| RSS (kB) | $rss_a | $rss_b | ${v_rss%% *} (${v_rss#* }) |"
sum "| descriptors | $fd_a | $fd_b | ${v_fd%% *} |"
sum "| threads | $th_a | $th_b | ${v_th%% *} |"
sum "| rounds / errors | - | - | $rounds / $errors |"
sum "| reconnections | - | - | $recon |"
say "RSS  ${rss_a} -> ${rss_b} kB  [$v_rss]"
say "FDs  ${fd_a} -> ${fd_b}       [$v_fd]"
say "THR  ${th_a} -> ${th_b}       [$v_th]"
say "traffic: $rounds rounds, $errors errors, $recon reconnection notice(s)"
[ "$recon" -gt 0 ] && { say "--- what plug said while reconnecting ---"; grep -i "reconnect" "$work/plug.log" | head -5; }

bad=0
case "$v_rss" in GROWS*) say "--- FAIL: memory keeps climbing after hours, which is the shape of a leak"; bad=1 ;; esac
case "$v_fd"  in GROWS*) say "--- FAIL: descriptors keep climbing, so something is not being closed"; bad=1 ;; esac
case "$v_th"  in GROWS*) say "--- FAIL: threads keep climbing, so goroutines are piling up"; bad=1 ;; esac
[ "$errors" -gt 0 ] && { say "--- FAIL: $errors round(s) did not answer 200, in a cluster that never moved"; bad=1; }
# A run that measured nothing is not a run that found nothing. One round every
# two seconds plus latency, so a quarter of the arithmetic is a floor no healthy
# session can miss, and no broken one can meet.
expected=$(( SOAK_MINUTES * 60 / 8 ))
[ "$rounds" -lt "$expected" ] && { say "--- FAIL: only $rounds rounds in ${SOAK_MINUTES} min, expected at least $expected. The numbers above measured an idle process, not a working session"; bad=1; }
kill -0 "$plug_pid" 2>/dev/null || { say "--- FAIL: the session did not survive the run"; bad=1; }
if [ "$bad" = 0 ]; then sum "**soak** ✅"; say "soak OK"; else sum "**soak** ❌"; fi
exit "$bad"
