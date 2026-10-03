#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# matrix: the protocol grid, every language client UNDER plug reaching each
# cluster service BY NAME over the mesh. Sourced by scripts/ci/e2e-matrix.sh
# after lib.sh.
#
# matrix_lang <lang> <port-base> <index> - ONE language against all eight
# protocols, under a name of its own so it can run beside the other three.
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
    for _ in 1 2; do
      ml_out="$(plug_serving "$ml_serve" $("cmd_$ml_l") "$ml_proto" "$ml_target" 2>&1)"
      if printf '%s' "$ml_out" | grep -q "E2E-OK"; then ml_r=PASS; break; fi
      sleep 2
    done
    echo "RESULT $ml_l $ml_proto $ml_r"
    [ "$ml_r" = PASS ] && continue
    echo "--- $ml_l / $ml_proto FAIL ---"; printf '%s\n' "$ml_out" | tail -8 | sed 's/^/    /'
    # go-on-mac only: the failure pattern (5/8 pass) rules out a plain "wrong
    # resolver" story - capture, INSIDE a live plug session, what the system
    # resolver config looks like and which resolver path Go actually takes.
    if [ "$ml_l" = go ] && [ "$os" = mac ]; then
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
  local fails=0 results="" entry l
  # A client that never built is NOT a cell to skip quietly. do_setup reports the
  # build failure and still exits 0, so those cells used to render as "·" and the
  # step went GREEN having proven nothing - 8 of 32 cells unrun for one language,
  # all 32 if Maven and pip were both down. Name them and fail here, where the
  # grid is rendered, rather than shipping a matrix that tested less than it says.
  local missing=""
  for l in $LANGS; do
    case " $built " in *" $l "*) : ;; *) missing="$missing $l" ;; esac
  done
  if [ -n "$missing" ]; then
    echo "--- matrix FAIL - client(s) never built:$missing"
    echo "    the build log is in the setup step (/tmp/build-<lang>.log)"
    sum "**protocol matrix** ❌ - client(s) that never built:$missing"
    return 1
  fi
  # The four languages run AT THE SAME TIME, each with its own name and port.
  #
  # Measured before this: 187s for 32 invocations - 5.8s each - of which the
  # protocol exchange is a sliver. What costs is starting a session: the SSH
  # connection, the datapath, provisioning the name. Thirty-two of those, one
  # after another, to run eight protocols four ways.
  #
  # Each language keeps its protocols in ORDER (its eight run one after another,
  # against one cluster) - what overlaps is the four languages, which have
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
    echo "#### protocol matrix - $(uname -s) · by name over the mesh"
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
