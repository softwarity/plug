#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# privilege drop: the privilege the child does NOT get. plug holds root on
# macOS (setuid) or file capabilities on Linux, and drops none of it for its own
# work: the drop exists for YOUR command, one level further down. Until this
# cell that was a comment and nothing else. The whole harness contained no
# `id -u` and no `whoami`, so the single most important property of a tool that
# runs your code with a privilege you do not have was asserted nowhere.
#
# Two questions, because there are two ways to hold privilege here. The child's
# UID must be the caller's, which is what a setuid macOS launcher would leak.
# And on Linux its effective capability set must be EMPTY, which is what the
# AMBIENT set leaks past an exec: a leak that was real until the release that
# added this cell, on any launch that did not need a private resolv.conf.
# Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
do_privdrop() {
  echo "=== privilege drop: the child runs as you, with nothing plug holds ==="
  local caller_uid child_out child_uid child_caps
  caller_uid="$(id -u)"
  child_out="$(plug_bounded 45 --host "$ip" --port "$port" $serve \
    bash -c 'id -u; grep -i "^CapEff" /proc/self/status 2>/dev/null || echo "CapEff: n/a"' 2>/dev/null | tr -d '\r')"
  child_uid="$(printf '%s\n' "$child_out" | sed -n '1p')"
  child_caps="$(printf '%s\n' "$child_out" | sed -n '2p' | tr -d '[:space:]')"

  if [ "$caller_uid" = 0 ]; then
    # Nothing to prove: the caller IS root, so a root child leaks nothing. Said
    # out loud rather than passed, or the cell reports a success it never measured.
    echo "privilege drop NOT MEASURABLE: this leg runs as root, so a root child proves nothing"
    sum "**privilege drop** (not measurable, leg runs as root)"
  elif [ "$child_uid" != "$caller_uid" ]; then
    echo "--- privilege FAIL: the child ran as uid '${child_uid:-nothing}', you are $caller_uid"
    echo "    plug is setuid or capability-granted and must hand your command YOUR identity, never its own"
    sum "**privilege drop (child runs as the caller)** FAILED, uid \`${child_uid:-none}\`"; return 1
  elif printf '%s' "$child_caps" | grep -qiE '^CapEff:0*[1-9a-f]'; then
    # Only a HEX MASK is judged. macOS has no /proc and answers "n/a"; reading
    # that as a non-empty set failed every macOS leg on the first draft, caught by
    # running the branch table before running a cluster.
    echo "--- privilege FAIL: the child kept capabilities: $child_caps"
    echo "    plug raises an AMBIENT set so its own caps survive exec'ing the core; your command must inherit none"
    sum "**privilege drop (no capabilities inherited)** FAILED, \`$child_caps\`"; return 1
  else
    echo "privilege drop OK: child ran as uid $caller_uid, capabilities ${child_caps:-n/a}"
    sum "**privilege drop (caller's uid, no capabilities)** OK"
  fi
}
