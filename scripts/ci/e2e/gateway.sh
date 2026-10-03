#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# gateway. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# gateway callback (reverse, driven from OUTSIDE): an EXTERNAL caller POSTs to the
# cluster's PUBLISHED gateway, which calls a -s name INSIDE the cluster that lands
# on our sink; the sink answers "<path> <id>" back. Two calls: root and deep path.
do_gateway() {
  local gwname gwcport gwlocal=18096
  gwname="gwsink-$leg" gwcport="$gw_cport"
  echo "=== gateway callback: external POST → gateway → $gwname → our sink :$gwlocal ==="
  if ! helper_bin sink; then
    echo "--- gateway FAIL - sink did not build"; sum "**gateway callback** ❌ (build)"; return 1
  fi
  # gw_post <json-body> - one POST to the published gateway, the answer's last line.
  gw_post() {
    curl -s --max-time 10 -X POST "http://$ip:18090/call" \
      -H 'content-type: application/json' -d "$1" 2>>/tmp/gw-post.err | tr -d '\r' | tail -1
  }
  # gw_call <expected> <json-body> - POST to the published gateway, retry (the
  # dynamic name needs a beat to exist), echo PASS or FAIL|<got>.
  gw_call() {
    local want="$1" body="$2"
    retry_until 3 3 "$want" gw_post "$body" && { echo PASS; return; }
    # Three attempts spanning ~39s all landed inside ONE tailnet outage on the
    # 2.11.0 release run, and the sink's own log recorded
    # "re-provisioned and verified after reconnect" seconds after this cell had
    # already given up. Retrying harder would be guessing at a duration for the
    # third time; ask the session instead. plug says when its transport dropped
    # ("keepalive: agent unreachable", then "agent connection re-established")
    # and when the name is back ("re-provisioned and verified after reconnect"),
    # so judge after it says so - and only then. The first version of this
    # looked for "re-arming", a word the CLI never writes, and so never waited.
    if grep -qE "agent unreachable|connection re-established" /tmp/gw.out 2>/dev/null; then
      echo "    (the session is reconnecting after a transport blip - waiting for it to say it is back)" >&2
      for _ in $(seq 1 20); do
        grep -q "re-provisioned and verified" /tmp/gw.out 2>/dev/null && break
        sleep 3
      done
      retry_until 3 3 "$want" gw_post "$body" && { echo PASS; return; }
    fi
    echo "FAIL|$ru_out"
  }
  "$PLUG" --host "$ip" --port "$port" -s "$gwname:$gwcport:$gwlocal" \
    "$root/sink$ext" -addr "127.0.0.1:$gwlocal" >/tmp/gw.out 2>&1 &
  local gw_pid=$! gw=FAIL gwpath=FAIL r
  sleep 8 # arm + provision the name + verify
  # 1) root call - sink answers "/ <id>"
  local gwnonce="cb-$gwname-$RANDOM"
  r="$(gw_call "/ $gwnonce" "{\"service\":\"$gwname\",\"port\":\"$gwcport\",\"id\":\"$gwnonce\"}")"
  if [ "$r" = PASS ]; then gw=PASS; echo "gateway OK - external POST reached our sink at / (id round-tripped)"
  else echo "--- gateway FAIL - got '${r#FAIL|}' (want '/ $gwnonce')"; fi
  # 2) deep-path call - sink answers "/hook/<n> <id>", proving the path travelled too
  local gwpnonce="cbp-$gwname-$RANDOM" gwpath_seg
  gwpath_seg="hook/$gwpnonce"
  r="$(gw_call "/$gwpath_seg $gwpnonce" "{\"service\":\"$gwname\",\"port\":\"$gwcport\",\"path\":\"$gwpath_seg\",\"id\":\"$gwpnonce\"}")"
  if [ "$r" = PASS ]; then gwpath=PASS; echo "gateway path OK - the full path /$gwpath_seg reached our sink intact"
  else echo "--- gateway path FAIL - got '${r#FAIL|}' (want '/$gwpath_seg $gwpnonce')"; fi
  [ "$gw" = PASS ] && [ "$gwpath" = PASS ] || { echo "    --- sink session ---"; tail -12 /tmp/gw.out 2>/dev/null | sed 's/^/    /'; tail -6 /tmp/gw-post.err 2>/dev/null | sed 's/^/    /'; }
  stop_bg "$gw_pid" "the gateway -s session"
  sum "**gateway callback** $(glyph "$gw") · **gateway path** $(glyph "$gwpath")"
  [ "$gw" = PASS ] && [ "$gwpath" = PASS ]
}
