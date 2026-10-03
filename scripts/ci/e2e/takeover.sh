#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# takeover. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# takeover: -s on a name a REAL deployed service owns PARKS it BY DEFAULT
# (container stopped, traffic lands on our local process) and RESTORES it when
# the session ends - no flag. Target = this leg's own tko-<leg> service
# (parking a shared one would break the other legs) on this leg's own PORT
# (the -s remote-forward binds that port on the agent globally, and the legs
# run concurrently - a shared port made the second leg's forward be denied by
# the agent). The prober is the in-cluster witness: what does
# http://tko-<leg>:<port>/ answer - before, during, after.
do_takeover() {
  local tname="$tko_name" tport="$tko_port"
  echo "=== takeover: park the deployed $tname, serve ours, restore ==="
  if ! helper_bin echo-local; then
    echo "--- takeover FAIL - echo-local did not build"; sum "**takeover (park+restore)** ❌ (build)"; return 1
  fi
  probe() { prober_fetch "http://$tname:$tport/"; }

  # Baseline: the deployed service answers through the cluster.
  if ! retry_until 3 3 "deployed-$tname" probe; then
    echo "--- takeover FAIL - baseline: prober said '${ru_out:-nothing}' (want deployed-$tname)"
    sum "**takeover (park+restore)** ❌ - baseline"; return 1
  fi

  # Take it over - the DEFAULT, no flag: our local echo must now answer the
  # SAME in-cluster URL. The echo's -ttl ends the session NATURALLY (child
  # exits → plug tears down and restores) - a `kill` on Windows/Git Bash is a
  # TerminateProcess that would skip the teardown, and the restore is exactly
  # what this cell asserts.
  "$PLUG" --host "$ip" --port "$port" -s "$tname:$tport:18096" \
    "$root/echo-local$ext" -addr 127.0.0.1:18096 -text "local-$tname" -ttl 75s >/tmp/takeover.out 2>&1 &
  local tko_pid=$! during="" t0=$(date +%s)
  # 75s, not 36: the ten reads below are ten `plug curl` sessions of a second
  # or two each, after the wait for the first local answer. At 36s the session
  # ended UNDER the reads, the restore put the deployed workload back, and the
  # tail of the ten read "deployed" - which looks exactly like a split route
  # and is not one. Every answer is stamped with its second for that reason.
  sleep 8 # arm + park + end-to-end verify
  retry_until 3 3 "local-$tname" probe; during="$ru_out"
  # Once the name answers at all, every read must be ours (see assert_all for
  # the split route this caught). Up to ten, but only while the session is
  # certainly alive: a read costs 8s on a Windows runner, so ten of them
  # outlive the 75s echo. Reads stop at 60s, and at least five must have fitted.
  local seq_during=""
  if [ "$during" = "local-$tname" ]; then
    aa_clock=$t0
    assert_all probe "local-$tname" 10 "deployed- local-" $((t0 + 60)) 5 || during="$aa_why"
    seq_during="$aa_seq"
  fi
  wait_bg "$tko_pid" "the takeover session (ends on its own -ttl)" 120

  # Session over: the deployed service must be back (its container restarts),
  # and it alone: ten reads, all deployed, or the restore left our route in.
  local after=""
  retry_until 5 3 "deployed-$tname" probe; after="$ru_out"
  # After the first deployed answer the controllers are still swapping slices
  # (the selector's own slice built, plug's mirrored one withdrawn): let it
  # settle, then ten reads. assert_all retries a blink once; what fails is a
  # "local-" answer (our route left in) or an error that stays.
  if [ "$after" = "deployed-$tname" ]; then
    sleep 3
    assert_all probe "deployed-$tname" 10 "deployed- local-" || after="$aa_why"
  fi

  if [ "$during" = "local-$tname" ] && [ "$after" = "deployed-$tname" ]; then
    echo "takeover OK - parked (every read while it lived came to us:$seq_during), then restored (10/10 deployed answers again)"
    sum "**takeover (park+restore)** ✅"; return 0
  fi
  echo "--- takeover FAIL - during='$during' (want local-$tname, every time) after='$after' (want deployed-$tname, every time)"
  echo "    --- takeover session output ---"; tail -12 /tmp/takeover.out 2>/dev/null | sed 's/^/    /'
  sum "**takeover (park+restore)** ❌ - during \`${during:-nothing}\` · after \`${after:-nothing}\`"; return 1
}
