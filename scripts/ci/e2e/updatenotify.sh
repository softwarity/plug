#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# updatenotify. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# update-notify: the BACKGROUND check, end to end - the one thing the update
# cells never covered.
#
# It runs inside the CORE. For most of this cell's life the core here was the N-1
# one, since the core is whatever the AGENT serves, and that is why the cell was
# written twice and pulled twice: N-1 either had no check (2.7.3) or had a broken
# one (2.9.0 asked `version` on the tunnel channel, where the verb does not
# exist).
#
# That is no longer what runs. plug refuses to execute a core carrying no release
# signature, and every release before signing carries none, so against those the
# launcher falls back to its own build and THIS branch's core is what performs the
# check. The cell still measures the thing it is named for, end to end against a
# real cluster whose agent is genuinely one release behind. It stops measuring N-1
# specifically, which is a loss worth naming here rather than leaving the comment
# above to claim something that stopped being true. It comes back on its own once
# N-1 is itself a signed release.
#
# Shape imposed by the design: the check settles for 10s, then dials, asks the
# agent `info` and queries the registry - so the session has to LAST. And its
# verdict is deliberately not announced by the session that found it (that one is
# busy running your command); it is read by the NEXT launch. Two sessions, then.
do_updatenotify() {
  local oldport="$prev_port"
  echo "=== update-notify (cluster B): a session must FIND the newer release, the next one must SAY it ==="
  local ip_b
  ip_b="$(wait_cluster "$peer_b")" || { echo "cluster $peer_b unreachable" >&2; sum "**update notify** ❌ (cluster B)"; return 1; }

  local prev
  prev="$(ssh_agent "$ip_b" "$oldport" version 2>/dev/null | tr -d '\r')"
  # A FAIL, as in update-jump: the previous-release agent is a fixture this
  # cluster deploys on purpose, and a fixture that does not answer is the
  # cluster being wrong, not a reason to skip. The two cells used to disagree
  # on this (SKIP here, FAIL there) for the same agent on the same port.
  if ! printf '%s' "$prev" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "--- update-notify FAIL - the previous-release agent answered '${prev:-nothing}', not an x.y.z release"
    sum "**update notify** ❌ - no usable N-1 (answered \`${prev:-nothing}\`)"; return 1
  fi
  # The precondition that sank this cell twice, now verified instead of hoped:
  # the check itself must exist AND work in the core this agent serves.
  case "$prev" in
    2.7.*|2.8.*|2.9.0|2.9.1)
      echo "--- update-notify SKIP - N-1 is $prev, whose background check predates the 05/08 fix (needs >= 2.9.2)"
      sum "**update notify** – (N-1 $prev too old)"; return 0 ;;
  esac
  echo "    N-1 is $prev - its core carries the fixed check"

  if [ ! -x "$root/echo-local$ext" ] && ! helper_bin echo-local; then
    echo "--- update-notify FAIL - echo-local did not build"; sum "**update notify** ❌ (build)"; return 1
  fi


  # Session 1 - long enough for the check to settle, dial, ask `info` and reach
  # the registry. It writes the verdict; it does not announce it.
  # -c: a pure client. Nothing to name and no port to reserve - this cell is
  # about the background check, not about being reachable. (-s or -c is
  # mandatory since 2.0: plug refuses to guess what a process is to the cluster.)
  "$PLUG" --host "$ip_b" --port "$oldport" -c \
    "$root/echo-local$ext" -addr 127.0.0.1:18141 -text "notify-$leg" -ttl 40s >/tmp/notify1.out 2>&1 || true

  # Session 1 records its verdict in ~/.plug/update-<hash of host:port>; session 2
  # does nothing but read it. Waiting for that file to actually carry one is the
  # difference between a cell that fails on a race and one that says WHICH half
  # did not happen, and it costs nothing in the normal case, where the check has
  # long finished before the session's ttl runs out. The cell failed once with
  # "the second launch said nothing", which is true and tells you nothing: the
  # question is whether there was anything to say.
  upd_waited=0
  while [ "$upd_waited" -lt 30 ] && ! grep -hq "^available=." "$HOME"/.plug/update-* 2>/dev/null; do
    upd_waited=$((upd_waited + 1)); sleep 1
  done
  if ! grep -hq "^available=." "$HOME"/.plug/update-* 2>/dev/null; then
    echo "    session 1 recorded NO verdict, after its 40s plus ${upd_waited}s of waiting - so there is nothing for session 2 to announce, and the check is what to look at"
  elif [ "$upd_waited" -gt 0 ]; then
    echo "    session 1's verdict landed ${upd_waited}s after the session ended"
  fi

  # Session 2 - the launcher reads what session 1 recorded, on its way past.
  local out2 rc2=0
  out2="$("$PLUG" --host "$ip_b" --port "$oldport" -c \
          "$root/echo-local$ext" -addr 127.0.0.1:18142 -text "notify2-$leg" -ttl 3s </dev/null 2>&1)" || rc2=$?
  printf '%s\n' "$out2" | sed 's/^/    /'

  # An invocation plug REFUSES prints its usage and exits - which reads as "said
  # nothing about an update" and sent the first version of this cell chasing the
  # check for nothing. Name that case for what it is.
  if printf '%s' "$out2" | grep -q "tell plug what this process is"; then
    echo "--- update-notify FAIL - plug refused the invocation (missing -s/-c), so no session ever ran"
    sum "**update notify** ❌ - invocation refused"; return 1
  fi
  if ! printf '%s' "$out2" | grep -q "update available"; then
    # Before blaming the code: could this machine do what the check does?
    #
    # The check lists the repository's tags FROM HERE and has no fallback - by
    # design ("a timeout there is not a slower path, it is no check at all,
    # silently"). On the GitHub macOS runners Docker Hub times out, so the check
    # correctly finds nothing and there is nothing to announce. The cell cannot
    # tell that apart from a defect unless it tries the same request itself.
    #
    # Tried HERE, after the fact, rather than predicted before: a first version
    # probed /v2/ up front, and one leg sailed past that probe and failed anyway
    # - reachability at one instant does not predict a token exchange plus a tag
    # listing a minute later. Only the real request, at the moment it matters,
    # settles it.
    local tok tags
    tok="$(curl -s --max-time 10 "https://auth.docker.io/token?service=registry.docker.io&scope=repository:softwarity/plug:pull" 2>/dev/null \
           | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
    tags=""
    [ -n "$tok" ] && tags="$(curl -s --max-time 20 -H "Authorization: Bearer $tok" \
                              "https://registry-1.docker.io/v2/softwarity/plug/tags/list?n=1" 2>/dev/null)"
    if ! printf '%s' "$tags" | grep -q '"tags"'; then
      # The runner's network, not plug: counted and annotated as a SKIP, so it
      # cannot be read as a pass (it was, for every macOS leg, for months).
      skip_cell "update notify" "this runner cannot list the repository's tags (the same request the check makes, and it has no fallback)"
      return 0
    fi
    echo "--- update-notify FAIL - the second launch said nothing about an update, while the agent runs $prev, a newer release is published, AND this machine can reach the registry"
    echo "    (session 1 output follows)"; sed 's/^/    /' /tmp/notify1.out 2>/dev/null | tail -20
    sum "**update notify** ❌ - nothing announced"; return 1
  fi
  # It must name a release NEWER than the one running - announcing the version
  # already deployed would be worse than silence.
  local named
  named="$(printf '%s' "$out2" | sed -n 's/.*update available: v\([0-9][0-9.]*\).*/\1/p' | head -1)"
  if [ -z "$named" ] || [ "$named" = "$prev" ]; then
    echo "--- update-notify FAIL - announced '${named:-nothing}' while running $prev"
    sum "**update notify** ❌ - announced ${named:-nothing}"; return 1
  fi
  echo "update-notify OK - running $prev, a session found $named and the next launch announced it (rc $rc2)"
  sum "**update notify** ✅ - $prev → $named announced by the following launch"
}
