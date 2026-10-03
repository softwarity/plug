#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# resilience. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# resilience: the M5 bench's crash-recovery chain, replayed in CI - on cluster
# B, against a PER-LEG crash-test agent (res-agent-<leg>, its own published
# port): the three legs run concurrently, and interleaved restarts of a SHARED
# agent tore each other's teardowns apart the one time the legs aligned. A
# takeover session holds res-tko-<leg> through its own agent; the chaos service
# RESTARTS THAT AGENT mid-session; the keepalive must detect the dead
# transport, the rebooted agent's boot-gc restore the parked service, the
# reconnect re-arm -s and RE-PARK it - traffic back on the runner - and the
# session end restore the deployed service for good. The prober is reached
# through the MAIN agent, which never reboots - a witness that cannot blink.
do_resilience() {
  local rname="$res_name" rport="$res_port" ragent="$res_agent" rsshport="$res_sshport"
  echo "=== resilience (cluster B): park $rname via $ragent, RESTART that agent, re-park, restore ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**resilience (agent crash)** ❌ (cluster B)"; return 1; }
  if ! helper_bin echo-local; then
    echo "--- resilience FAIL - echo-local did not build"; sum "**resilience (agent crash)** ❌ (build)"; return 1
  fi
  bprobe() { prober_fetch "http://$rname:$rport/" "$ip_b"; }

  if ! retry_until 3 3 "deployed-res-$leg" bprobe; then
    echo "--- resilience FAIL - baseline: prober said '${ru_out:-nothing}' (want deployed-res-${leg})"
    sum "**resilience (agent crash)** ❌ - baseline"; return 1
  fi

  # Hold the takeover THROUGH THIS LEG'S OWN AGENT, with a tight keepalive so
  # the dead transport is detected in seconds; -ttl ends the session naturally
  # (Windows: kill would skip the teardown - see do_takeover).
  # A SECOND name on the same session, this one owned by nobody: no deployed
  # workload, so no parking receipt - which is the condition under which the
  # signpost is reused instead of replaced. $rname cannot answer that question,
  # since taking it over is exactly the case that must still replace it (the
  # receipt is what scales the parked workload back up).
  local vipname="vip-$leg"
  # 180s of life, not 150: the reads below are plug sessions of up to 8s each
  # on a Windows runner, and five of them follow the kill-sessions blip (20s)
  # and the keepalive's own detection of the crash (10-15s). At 150s the last
  # of them landed after the restore and read "deployed", which looks exactly
  # like a failed re-park and is not one. Every read is bounded by the
  # session's own clock (res_deadline) for the same reason.
  PLUG_KEEPALIVE_SECS=5 "$PLUG" --host "$ip_b" --port "$rsshport" -s "$rname:$rport:18123" -s "$vipname:9099:18123" \
    "$root/echo-local$ext" -addr 127.0.0.1:18123 -text "local-res-$leg" -ttl 180s >/tmp/resilience.out 2>&1 &
  local res_pid=$! during="" after_crash="" after="" t0 res_deadline
  t0=$(date +%s); res_deadline=$((t0 + 160)); aa_clock=$t0
  sleep 8
  retry_until 3 3 "local-res-$leg" bprobe; during="$ru_out"
  # Five reads, all ours, while the session lives (see assert_all): one
  # matching answer said nothing about the half of the requests that could
  # still be reaching the deployed pod.
  if [ "$during" = "local-res-$leg" ]; then
    assert_all bprobe "local-res-$leg" 5 "deployed- local-" "$res_deadline" || during="$aa_why"
  fi

  # The address a workload in the cluster resolves the name to, RIGHT NOW -
  # asked from inside, because that is the address callers cache and keep using.
  # On k8s the chaos service answers in `default` while the per-leg Services live
  # in plug-res-<leg>, and a BARE name does not cross namespaces - the lookup
  # failed identically before and after, which is what made this "NOT
  # MEASURABLE". The FQDN crosses; the other families have one flat space and
  # take the name as-is.
  local vipq="$vipname"
  [ "$family" = k8s ] && vipq="$vipname.plug-res-$leg.svc.cluster.local"
  cresolve() { plug_to "$ip_b" curl -s --max-time 10 "http://chaos:8095/resolve?name=$vipq" 2>/dev/null | tr -d '\r' | tail -1; }
  local addr_before addr_after
  addr_before="$(cresolve)"

  # Before killing the agent: the reconnect with NO death - the transport drops
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
  # so NOTHING reconnects - and an unchanged address would then read as proof
  # when it is merely the absence of an event. Say that, rather than let a
  # reader take silence for evidence.
  if ! grep -q killing /tmp/killsess.out 2>/dev/null; then
    echo "live-reconnect: NOT EXERCISED here - $(head -c 90 /tmp/killsess.out 2>/dev/null | tr -d '\r\n')"
    sum "**name keeps its address across a live reconnect** · not exercised on this family"
  else
  sleep 20 # keepalive (5s cadence here) notices, reconnect re-arms and re-provisions
  raddr_after="$(cresolve)"
  if ! is_addr "${raddr_before:-}" || ! is_addr "${raddr_after:-}"; then
    # Swarm is where this is asserted (a service VIP), so a reading that is
    # not an address there is the cell failing to measure, in red. Compose
    # only reports the pair, so it only reports that it could not.
    if [ "$family" = swarm ]; then
      echo "--- live-reconnect FAIL - the address could not be read: '${raddr_before:-nothing}' -> '${raddr_after:-nothing}'"
      sum "**name keeps its address across a live reconnect** ❌ - not measured: \`${raddr_before:-nothing}\` -> \`${raddr_after:-nothing}\`"
      addr_bad=1
    else
      echo "live-reconnect address: NOT MEASURABLE on $family - '${raddr_before:-nothing}' -> '${raddr_after:-nothing}'"
      sum "**name keeps its address across a live reconnect** · not measurable on this family"
    fi
  else
    case "$family" in
      swarm)
        if [ "$raddr_before" = "$raddr_after" ]; then
          echo "live-reconnect OK - $vipname kept $raddr_before while its agent stayed up (signpost reused in place)"
          sum "**name keeps its address across a live reconnect** ✅ \`$raddr_before\`"
        else
          echo "--- live-reconnect FAIL - $vipname moved from $raddr_before to $raddr_after on a mere transport blip"
          sum "**name keeps its address across a live reconnect** ❌ - \`$raddr_before\` → \`$raddr_after\`"
          addr_bad=1
        fi ;;
      *)
        echo "live-reconnect address: $raddr_before → $raddr_after (reported on this family)"
        sum "**name address across a live reconnect** · \`$raddr_before\` → \`$raddr_after\`" ;;
    esac
  fi
  fi

  # Crash THIS LEG'S agent mid-session (the chaos service answers, then fires).
  #
  # The answer is REQUIRED. chaos says "restarting" before it fires, and
  # anything else (a 500, a 501, an empty body from a session that could not
  # reach it) is a restart that did not happen, after which everything below
  # would be asserting a recovery from nothing. That is what the k8s legs did
  # for as long as this cell existed: chaos looked for a pod label no agent
  # carries, answered 500 "no pod labelled", the reply went to /dev/null, and
  # two green ticks were awarded per leg for a session that was never cut.
  local n_reconnect_before n_reconnect_after cut_proven=0
  n_reconnect_before="$(grep -c "after reconnect" /tmp/resilience.out 2>/dev/null || true)"
  plug_to "$ip_b" curl -s --max-time 10 "http://chaos:8095/restart-agent?svc=$ragent" >/tmp/restart.out 2>&1 || true
  if ! grep -q restarting /tmp/restart.out 2>/dev/null; then
    echo "--- resilience FAIL - chaos did not restart $ragent; it said: '$(head -c 200 /tmp/restart.out 2>/dev/null | tr -d '\r\n')'"
    wait_bg "$res_pid" "the resilience session" 220
    sum "**resilience (agent crash mid-session)** ❌ - the agent was never restarted (chaos said \`$(head -c 80 /tmp/restart.out 2>/dev/null | tr -d '\r\n')\`)"; return 1
  fi
  # keepalive detects (~10-15s at 5s cadence), reconnect re-arms and re-parks;
  # the rebooted agent's boot-gc restored the parked service in between.
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    after_crash="$(bprobe)"
    [ "$after_crash" = "local-res-$leg" ] && break
    sleep 5
  done
  # Proof that the transport actually died: the session says so itself when it
  # comes back ("re-provisioned and verified after reconnect", or one of the
  # WARNING forms, every one of which carries "after reconnect"). Counted
  # against what kill-sessions above already wrote, on the families where it
  # ran. A local answer with no new reconnect line is a session that was never
  # cut, whatever chaos answered, and this cell is about the cut.
  for _ in 1 2 3 4 5 6; do
    n_reconnect_after="$(grep -c "after reconnect" /tmp/resilience.out 2>/dev/null || true)"
    [ "${n_reconnect_after:-0}" -gt "${n_reconnect_before:-0}" ] && { cut_proven=1; break; }
    sleep 5
  done
  # Five reads, all ours again: the re-park is a takeover like any other and
  # a single matching read proves no more here than it did above. Three must
  # fit before the phase's deadline: a Windows read costs eight seconds and
  # the reconnect itself has already spent most of the window, so the fifth
  # read landed just past the deadline on a run where every read was right
  # (the first real run of this cell on Windows swarm). What this phase
  # asserts is "no read went to the deployed service", and three reads say
  # that as well as five; what it must never do is accept ONE.
  if [ "$after_crash" = "local-res-$leg" ]; then
    assert_all bprobe "local-res-$leg" 5 "deployed- local-" "$res_deadline" 3 || after_crash="$aa_why"
  fi
  addr_after="$(cresolve)"
  # Whether the name KEEPS its address across this depends on what the backend
  # does at agent boot. This cell RESTARTS the agent, and the boot gc sweeps that
  # agent's own signposts - so on Docker and Swarm there is nothing left to reuse
  # and the replacement gets a new address. Kubernetes keeps its Service, hence
  # its ClusterIP, which is why it alone can be asserted here.
  #
  # The OTHER reconnect - agent alive, transport dropped - is asserted a few
  # lines above via kill-sessions; this one is specifically the post-restart
  # case, where losing the address is correct.
  # An answer that is not an ADDRESS proves nothing, and comparing two identical
  # error strings would read as "kept" - a test that passes without testing,
  # which is worse than one that fails. It happened here: on k8s a BARE name did
  # not cross from chaos (in default) to the per-leg Services (in
  # plug-res-<leg>), so the lookup failed identically before and after - now
  # asked by FQDN. Any reading that is still not an address yields no verdict,
  # said out loud.
  if ! is_addr "$addr_before" || ! is_addr "$addr_after"; then
    # k8s is where this is asserted (a ClusterIP that must survive), so a
    # reading that is not an address there fails the cell in red rather than
    # sitting in a grey line beside the green ones.
    if [ "$family" = k8s ]; then
      echo "--- address across the agent restart could not be read: '${addr_before:-nothing}' -> '${addr_after:-nothing}'"
      sum "**name keeps its address across an agent restart** ❌ - not measured: \`${addr_before:-nothing}\` -> \`${addr_after:-nothing}\`"
      addr_bad=1
    else
      echo "address across the agent restart: NOT MEASURABLE on $family - '${addr_before:-nothing}' -> '${addr_after:-nothing}'"
      sum "**name address across an agent restart** · not measurable on this family"
    fi
  else
    case "$family" in
      k8s)
        if [ "$addr_before" = "$addr_after" ]; then
          echo "address kept across the agent restart - $vipname stayed at $addr_before"
          sum "**name keeps its address across an agent restart** ✅ \`$addr_before\`"
        else
          echo "--- $vipname moved from '$addr_before' to '$addr_after' - the k8s Service should have kept its ClusterIP"
          sum "**name keeps its address across an agent restart** ❌ - \`$addr_before\` → \`$addr_after\`"
          addr_bad=1
        fi ;;
      *)
        echo "address across the agent restart: $addr_before → $addr_after (boot gc sweeps the signpost - expected)"
        sum "**name address across an agent restart** · swept and rebuilt (\`$addr_before\` → \`$addr_after\`)" ;;
    esac
  fi

  # Bounded like the others: the -ttl is 180s, so 220 leaves room for a slow
  # teardown without ever becoming an unbounded wait. A session that ignores
  # its own ttl used to hold the leg until the job timeout, twenty-five
  # minutes later, with nothing in the log naming what it waited for.
  wait_bg "$res_pid" "the resilience session" 220

  # The deployed workload is coming back from a stop, on a runner that has just
  # restarted an agent under it - a "connection reset by peer" here is that
  # container answering mid-restart, not a failure to restore. 15s was enough
  # until it wasn't (macOS, while ubuntu passed the same cell in that run). Same
  # assertion, room to land.
  retry_until 15 3 "deployed-res-$leg" bprobe; after="$ru_out"
  # Then ten reads, all deployed: a restore that left our route in is the
  # orphan cell's incident, and this restore follows an agent reboot.
  if [ "$after" = "deployed-res-$leg" ]; then
    sleep 3
    assert_all bprobe "deployed-res-$leg" 10 "deployed- local-" || after="$aa_why"
  fi

  if [ "$during" = "local-res-$leg" ] && [ "$after_crash" = "local-res-$leg" ] && [ "$after" = "deployed-res-$leg" ] && [ "$addr_bad" = 0 ] && [ "$cut_proven" = 1 ]; then
    echo "resilience OK - parked, agent restarted (the session logged its reconnect), RE-parked (self-heal + boot-gc + re-arm), restored"
    sum "**resilience (agent crash mid-session)** ✅"; return 0
  fi
  echo "--- resilience FAIL - during='$during' after_crash='$after_crash' (want local-res-$leg, every read) after='$after' (want deployed-res-$leg, every read) reconnect-logged=$cut_proven (want 1)"
  [ "$cut_proven" = 1 ] || echo "    the session never logged a reconnect after chaos said 'restarting': the agent was not cut under it, so nothing above tested a recovery"
  echo "    --- session output ---"; tail -15 /tmp/resilience.out 2>/dev/null | sed 's/^/    /'
  # The agent it restarted is what restores the parked service through its boot
  # gc, so when the service does not come back the agent is the first suspect -
  # and it can say so itself.
  agent_state "$ip_b" "$ragent"
  sum "**resilience (agent crash mid-session)** ❌ - during \`${during:-nothing}\` · post-crash \`${after_crash:-nothing}\` · after \`${after:-nothing}\`"; return 1
}
