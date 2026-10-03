#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# expose. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# expose (reverse): serve a runner-local port under a cluster name (plug -s) and
# have a plain cluster workload (prober) fetch it. ONE name+port per OS leg.
do_expose() {
  local exname exposeport
  # Named from $leg, not from uname: two Linux legs (amd64 and arm64) would
  # otherwise claim the same name on a shared cluster - measured, and it took
  # the amd64 leg down with it. The port stays per-OS: it is local to this
  # machine, and two signposts may share a cluster port anyway (see sameport).
  exname="exposed-$leg" exposeport="$expose_port"
  echo "=== expose: $exname:$exposeport → this runner's :18086 ==="
  if ! helper_bin echo-local; then
    echo "--- expose FAIL - echo-local did not build"; sum "**expose (cluster→local)** ❌ (build)"; return 1
  fi
  "$PLUG" --host "$ip" --port "$port" -s "$exname:$exposeport:18086" \
    "$root/echo-local$ext" -addr 127.0.0.1:18086 -text "expose-ok-$exname" >/tmp/expose.out 2>&1 &
  local expose_pid=$! eo=""
  sleep 8 # arm + end-to-end verify (the session logs "path verified" into expose.out)
  ex_probe() { plug curl -s --max-time 10 "http://prober:8097/fetch?url=http://$exname:$exposeport/" 2>>/tmp/expose-probe.err | tr -d '\r' | tail -1; }
  retry_until 3 3 "expose-ok-$exname" ex_probe; eo="$ru_out"
  stop_bg "$expose_pid" "the -s session"
  if [ "$eo" != "expose-ok-$exname" ]; then
    echo "--- expose FAIL - prober said '${eo:-nothing}' (want expose-ok-$exname)"
    echo "    --- expose session output ---"; tail -12 /tmp/expose.out 2>/dev/null | sed 's/^/    /'
    tail -6 /tmp/expose-probe.err 2>/dev/null | sed 's/^/    /'
    sum "**expose (cluster→local)** ❌ - prober said \`${eo:-nothing}\`"; return 1
  fi
  echo "expose OK - a cluster workload reached this runner's local service by name"; sum "**expose (cluster→local)** ✅"

  # dns honesty for a KILLED name - the gateway-poisoning regression, reproduced
  # the way it bit: a name is served, a WITNESS session resolves it (seeding the
  # resolver's "this exists" verdict), the serving session dies, and the witness
  # asks again. plug used to keep answering "found" from a five-minute cache and
  # mint a fake address for a name that was gone - which, echoed into a Docker
  # Desktop cluster, left a real gateway dialling an address that only existed
  # on the workstation, until someone restarted it.
  #
  # The witness must be ONE LONG-LIVED session asking twice, not two sessions:
  # on Linux every session carries its own resolver cache, so a fresh session
  # would start clean and prove nothing - while on macOS and Windows the shared
  # daemon/service answers, which is exactly the incident's shape. One script,
  # both shapes, each OS through its real resolver.
  #
  # The serving session ends by TTL, never by kill: a kill skips the teardown on
  # Windows, and it is the TEARDOWN (unserve, signpost gone) that opens the gap
  # under test. Verdict by curl exit code - locale-proof where message text is
  # not: 6 = could not resolve (the honest answer), 7/28 = connected or hung on
  # a minted fake (the poison), 0 = still served (the teardown never ran).
  local pname="poison-$leg"
  echo "=== dns honesty for a killed name: $pname must be NXDOMAIN within seconds of its session dying ==="
  "$PLUG" --host "$ip" --port "$port" -s "$pname:9096:18087" \
    "$root/echo-local$ext" -addr 127.0.0.1:18087 -text "alive-$pname" -ttl 20s >/tmp/poison-serve.out 2>&1 &
  local poison_pid=$!
  # The address the CLUSTER resolves while the name is served - the one every
  # caller caches for the 600s TTL Docker's DNS hands out. Compared after a
  # relaunch below: the linger's whole contract is that it does not change.
  presolve() { plug curl -s --max-time 10 "http://chaos:8095/resolve?name=$pname" 2>/dev/null | tr -d '\r' | tail -1; }
  local paddr_before=""
  # 20s of life: the witness session takes 1-4s to arm (a cold Windows service
  # is the slow end), so its first probe lands around t=9-12 with several
  # seconds to spare, and its second around t=31 - the name then dead for
  # ~13s, past the 5s check TTL plus the 5s the OS may repeat our old answer.
  sleep 8
  # Captured in PARALLEL with the witness, not before it: presolve is a full
  # plug session (~5-8s on a cold Windows runner), and running it inline pushed
  # the witness past the serving session's ttl - P1 found nothing and the cell
  # could not conclude. The name is alive for both as long as the capture
  # overlaps the ttl window, which the background start guarantees.
  presolve >/tmp/poison-addr-before 2>/dev/null &
  local presolve_pid=$!
  local wout
  wout="$(perl -e 'alarm 60; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" -c bash -c \
    "p1=\$(curl -s --max-time 8 http://$pname:9096/ || true); echo \"P1=\$p1\"; sleep 22; curl -s --max-time 8 -o /dev/null http://$pname:9096/; echo \"P2-RC=\$?\"" 2>/dev/null | tr -d '\r')"
  wait_bg "$poison_pid" "the poison serving session" 90
  wait_bg "$presolve_pid" "the resolve probe" 60
  paddr_before="$(tr -d '\r' </tmp/poison-addr-before 2>/dev/null | tail -1)"
  local p1 p2rc
  p1="$(printf '%s' "$wout" | sed -n 's/^P1=//p' | head -1)"
  p2rc="$(printf '%s' "$wout" | sed -n 's/^P2-RC=//p' | head -1)"
  if [ "$p1" != "alive-$pname" ]; then
    echo "--- poison test FAIL - the witness never reached $pname while it was served (P1='${p1:-nothing}') - cannot conclude"
    sum "**dns honesty (killed name)** ❌ - witness never saw it alive"; return 1
  fi
  case "$p2rc" in
    6)
      echo "poison test OK - $pname answered, its session died, and the SAME witness got an honest resolution failure"
      sum "**dns honesty (killed name → NXDOMAIN)** ✅" ;;
    7|52)
      # The LINGER outcome (Swarm/k8s): the signpost outlives the session so the
      # name keeps its address, and a connect is accepted-then-closed (52) or
      # refused (7) INSTANTLY - a stopped service's semantics, benched at 0s.
      # What the fix removed is the hang on a minted fake, and that shows as 28.
      echo "poison test OK - $pname still resolves (lingering for relaunch) and fails fast (curl rc $p2rc)"
      sum "**dns honesty (killed name → fast refusal, address kept)** ✅" ;;
    0)
      echo "--- poison test FAIL - $pname still answers after its session's ttl: the teardown never freed the name"
      sum "**dns honesty (killed name)** ❌ - name still served"; return 1 ;;
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
      # Asked through chaos's own resolver, with the FQDN, which is the form
      # that is right whatever namespace chaos answers from (it lives in
      # `default`, k8s.res-agents.yaml, beside these names; the resilience cell
      # needs the FQDN for real, its VIP being in plug-res-<leg>). A Service
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
      echo "--- poison test FAIL - curl exit $p2rc: hang or fake on a dead name (the gateway-poisoning bug)"
      sum "**dns honesty (killed name)** ❌ - rc \`${p2rc:-none}\` on a dead name"; return 1 ;;
  esac

  # The linger's contract, proven the way the incident bit: relaunch the SAME
  # name and ask the cluster again. Swarm keeps its service VIP by in-place
  # update and k8s its ClusterIP - asserted. A plain-Docker signpost is a new
  # container (its relay target is baked into the entrypoint), so compose is
  # reported, never asserted.
  "$PLUG" --host "$ip" --port "$port" -s "$pname:9096:18087" \
    "$root/echo-local$ext" -addr 127.0.0.1:18087 -text "alive-$pname" -ttl 10s >/tmp/poison-serve2.out 2>&1 &
  local poison2_pid=$!
  sleep 7
  local paddr_after
  paddr_after="$(presolve)"
  wait_bg "$poison2_pid" "the relaunched poison session" 90
  if ! is_addr "${paddr_before:-}" || ! is_addr "${paddr_after:-}"; then
    # Not an address on either side. On compose that is a reading that cannot
    # be compared and is reported as such; on Swarm and k8s the assertion IS
    # measurable (a VIP, a ClusterIP), so a probe that could not read one is
    # the cell failing to measure what it is named for, and it says so in red
    # rather than in a grey line that sits next to the green ones.
    case "$family" in
      swarm|k8s)
        echo "--- linger FAIL - the address across the relaunch could not be read: '${paddr_before:-nothing}' -> '${paddr_after:-nothing}'"
        sum "**name keeps its address across a relaunch** ❌ - not measured: \`${paddr_before:-nothing}\` -> \`${paddr_after:-nothing}\`"
        return 1 ;;
      *)
        echo "address across the relaunch: NOT MEASURABLE on compose - '${paddr_before:-nothing}' -> '${paddr_after:-nothing}'"
        sum "**name keeps its address across a relaunch** · not measurable on this family"
        return 0 ;;
    esac
  fi
  case "$family" in
    swarm|k8s)
      if [ "$paddr_before" = "$paddr_after" ]; then
        echo "linger OK - $pname kept $paddr_before across kill and relaunch; a caller's 600s cache stays valid"
        sum "**name keeps its address across a relaunch** ✅ \`$paddr_before\`"
      else
        echo "--- linger FAIL - $pname moved from $paddr_before to $paddr_after across the relaunch"
        sum "**name keeps its address across a relaunch** ❌ - \`$paddr_before\` → \`$paddr_after\`"
        return 1
      fi ;;
    *)
      echo "address across the relaunch: $paddr_before → $paddr_after (plain docker recreates - reported)"
      sum "**name address across a relaunch** · docker recreates (\`$paddr_before\` → \`$paddr_after\`)" ;;
  esac

}
