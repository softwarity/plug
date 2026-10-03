#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# client-only (-c): consume the cluster, nothing served. The DB-tool shape: no
# name, no agent port, outbound only. Sourced by scripts/ci/e2e-matrix.sh after
# lib.sh.
do_clientonly() {
  echo "=== client-only (-c): consume the cluster, nothing served ==="
  # Two attempts, for the same reason do_matrix takes two: the mesh datapath can
  # blip on the first hit. This cell was single-shot and got away with it while
  # setup took 769s - twelve minutes of building clients during which the cluster
  # finished settling. Setup is now ~70s, so the first cell arrives while the
  # stack may still be coming up, and `wait_cluster` only proves the AGENT
  # answers, not that httpbin does. Shortening the run did not create the race;
  # it stopped hiding it.
  # -sS, and curl's stderr KEPT. It was `-s` with `2>/dev/null`, which threw away
  # the one thing worth having: 000 means "no HTTP response", and covers a name
  # that did not resolve, a connection refused and a timeout alike - three
  # different problems that plug answers three different ways. Twice this cell
  # went red on a 000 nobody could attribute, because the cause had been
  # discarded one character at a time.
  local co _try
  for _try in 1 2; do
    co="$(plug_bounded 45 --host "$ip" --port "$port" -c \
      curl -sS --max-time 10 -o /dev/null -w '%{http_code}' http://httpbin:8080/get 2>/tmp/client-only.err | tr -d '\r' | tail -1)"
    [ "$co" = "200" ] && break
    [ "$_try" = 1 ] && { echo "    (got '${co:-nothing}' - one retry, the datapath may still be settling)"; sleep 5; }
  done
  if [ "$co" = "200" ]; then
    echo "client-only OK - -c reached httpbin by name with nothing served"
    sum "**client-only (-c)** ✅"
  else
    echo "--- client-only FAIL - got '${co:-nothing}' (want 200)"
    echo "    curl said: $(tr -d '\r' < /tmp/client-only.err | tail -3 | tr '\n' ' ')"
    # Which of the three it is decides where to look: an unresolved name is the
    # pre-mint check saying the cluster does not have it; a refusal is the name
    # being there with nothing behind it; a timeout is neither, and would be the
    # only one of the three that points at plug.
    echo "    --- what plug thinks, right now ---"
    # doctor, NOT inside a plug session: it is read-only and needs no datapath,
    # and nesting it in the very session under suspicion would tell us about the
    # wrong thing (and can take longer than the answer is worth).
    plug_bounded 40 doctor 2>&1 \
      | grep -iE "resolver|daemon|datapath|agent|profile" | head -8 | sed 's/^/    /'
    sum "**client-only (-c)** ❌ - \`${co:-nothing}\` · $(tr -d '\r' < /tmp/client-only.err | tail -1)"; return 1
  fi
}
