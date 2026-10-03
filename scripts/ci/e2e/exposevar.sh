#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# exposevar. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# exposevar: the SAME reverse path, with the local port NAMED instead of pinned
# (-s <name>:<cluster-port>:PORT). plug allocates a free port, substitutes {PORT}
# in the command, and arms the mapping on that same number.
#
# This is the one check the unit tests cannot make: that the port the child binds
# and the port the tunnel forwards to are the SAME number. Get that wrong and
# nothing errors - echo-local listens happily on one port while the cluster name
# forwards to another, and the prober just gets nothing. Which is exactly what
# this asserts: a body, by name, through the cluster.
do_exposevar() {
  local exname exposeport
  exname="exposedvar-$leg" exposeport="$exposevar_port"
  echo "=== exposevar: $exname:$exposeport → a port plug picks, injected as {PORT} ==="
  if ! helper_bin echo-local; then
    echo "--- exposevar FAIL - echo-local did not build"; sum "**expose, named port (-s …:PORT)** ❌ (build)"; return 1
  fi
  # echo-local defaults to :18086 when -addr is unusable - so an unsubstituted
  # "{PORT}" cannot accidentally pass by landing on the pinned phase's port.
  "$PLUG" --host "$ip" --port "$port" -s "$exname:$exposeport:PORT" \
    "$root/echo-local$ext" -addr "127.0.0.1:{PORT}" -text "exposevar-ok-$exname" >/tmp/exposevar.out 2>&1 &
  local expose_pid=$! eo=""
  sleep 8
  ex_probe() { plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$exname:$exposeport/" 2>>/tmp/exposevar-probe.err | tr -d '\r' | tail -1; }
  retry_until 3 3 "exposevar-ok-$exname" ex_probe; eo="$ru_out"
  stop_bg "$expose_pid" "the -s session"
  if [ "$eo" = "exposevar-ok-$exname" ]; then
    echo "exposevar OK - allocated port, substituted in argv, and the mapping agreed with it"
    sum "**expose, named port (-s …:PORT)** ✅"; return 0
  fi
  echo "--- exposevar FAIL - prober said '${eo:-nothing}' (want exposevar-ok-$exname)"
  echo "    --- exposevar session output ---"; tail -12 /tmp/exposevar.out 2>/dev/null | sed 's/^/    /'
  tail -6 /tmp/exposevar-probe.err 2>/dev/null | sed 's/^/    /'
  sum "**expose, named port (-s …:PORT)** ❌ - prober said \`${eo:-nothing}\`"; return 1
}
