#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# dns relay: a question that is not an address must still be answered.
#
# plug used to reply NODATA to every SRV, MX, PTR and TXT. On macOS its stub is
# the resolver for the WHOLE machine while a session runs, so that broke AD
# clients, mongodb+srv:// URIs and Consul host-wide. The unit tests prove the
# relay and the no-leak rule with a fake upstream; what only a real machine can
# show is that a REAL resolver, reached through plug's stub on this OS, still
# answers. A not-found is the one verdict that fails: nothing but plug can
# produce it for a name that plainly has MX records.
# Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
do_dnsrelay() {
  echo "=== dns relay: a question that is not an address must still be answered ==="
  if [ -x "$(cmd_go)" ]; then
    local dr dout
    # The WHOLE output, and the verdict looked up in it - not `tail -1`. plug
    # refuses a held name over several lines, and the last of them reads "the
    # holder is on another machine or another account": taken alone it was
    # reported as a DNS relay failure, for a session that never started at all.
    dout="$(plug "$(cmd_go)" dns mx:google.com 2>&1 | tr -d '\r')"
    dr="$(printf '%s\n' "$dout" | grep -m1 '^E2E-OK' || printf '%s\n' "$dout" | tail -1)"
    case "$dr" in
      E2E-OK*) echo "dns relay OK - $dr"; sum "**dns relay (non-A → upstream)** ✅" ;;
      *)
        if printf '%s' "$dout" | grep -q "It frees itself once that session ends"; then
          # A refusal is not a relay verdict. Say which it is, because the two
          # send whoever reads this to opposite places.
          echo "--- dns relay FAIL - plug REFUSED to start: the name is still held, so the relay never ran"
          sum "**dns relay (non-A → upstream)** ❌ - plug refused to start (name still held)"
        else
          echo "--- dns relay FAIL - ${dr:-<nothing>}"
          sum "**dns relay (non-A → upstream)** ❌ - \`${dr:-nothing}\`"
        fi
        printf '%s\n' "$dout" | tail -8 | sed 's/^/    /'
        return 1 ;;
    esac
  else
    echo "dns relay SKIP - the go client was not built on this leg"
    sum "**dns relay (non-A → upstream)** ·"
  fi
}
