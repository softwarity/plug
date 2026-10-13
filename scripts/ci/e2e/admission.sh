#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# admission. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# admission: an agent deployed with PLUG_ADMISSION=invite lets in only the keys
# a member accepted, lived the way two developers live it. Two profiles on this
# runner stand for two machines, each with the key plug makes for it: the first
# to connect becomes the first member, the second is recorded and refused with
# the line to send, the member accepts it, the agent restarts and still knows
# both, the newcomer reaches a service by name, the member revokes the key, and
# the newcomer is refused again.
#
# Against THIS LEG'S adm-agent on cluster B (per leg, like the update cells: the
# first key to connect becomes the first member, so a shared agent would test
# a race between legs). Its members live where the family keeps them, a volume,
# Swarm configs or the published manifest's Secret, which is what the restart
# reads back. On macOS and Windows the sessions ride the shared daemon, whose
# open tunnel the revocation must cut: the last refusal proves that too.
adm_member=adm-member
adm_newcomer=adm-newcomer

do_admission() {
  local rc=0
  admission_run || rc=$?
  # The two profiles go with the cell: a later one that runs plug with no -p
  # must not meet a picker between three profiles.
  rm -f "$HOME/.plug/$adm_member.conf" "$HOME/.plug/$adm_newcomer.conf" \
        "$HOME/.plug/keys/$adm_member" "$HOME/.plug/keys/$adm_member.pub" \
        "$HOME/.plug/keys/$adm_newcomer" "$HOME/.plug/keys/$adm_newcomer.pub"
  return "$rc"
}

admission_run() {
  local aagent="$adm_agent" aport="$adm_sshport" ip_b out rc short
  adm_fail() {
    echo "--- admission FAIL - $1"
    printf '%s\n' "$out" | tail -10 | sed 's/^/    /'
    sum "**admission by approval** ❌ - $1"
  }
  echo "=== admission (cluster B): $aagent lets in only the keys a member accepted ==="
  ip_b="$(wait_cluster "$peer_b")" || { out=""; adm_fail "cluster $peer_b unreachable"; return 1; }
  wait_agent "$ip_b" "$aport" || { out=""; adm_fail "$aagent never answered on $aport"; return 1; }

  mkdir -p "$HOME/.plug"
  printf 'host = %s\nport = %s\n' "$ip_b" "$aport" > "$HOME/.plug/$adm_member.conf"
  printf 'host = %s\nport = %s\n' "$ip_b" "$aport" > "$HOME/.plug/$adm_newcomer.conf"

  # 1. The first key to connect becomes the first member. The agent refuses the
  # key built into plug, plug makes the profile one of its own and asks again.
  rc=0; out="$(plug_bounded 60 -p "$adm_member" requests </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  [ "$rc" = 0 ] || { adm_fail "the first member's requests exited $rc"; return 1; }
  printf '%s' "$out" | grep -q "a key of its own" || { adm_fail "plug did not make the first member a key of its own"; return 1; }
  printf '%s' "$out" | grep -q "first member" || { adm_fail "the first key was not welcomed as the first member"; return 1; }
  printf '%s' "$out" | grep -q "no request is waiting" || { adm_fail "a fresh agent already had requests"; return 1; }

  # 2. The second key is recorded and refused, with the line to send.
  rc=0; out="$(plug_bounded 60 -p "$adm_newcomer" requests </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  [ "$rc" != 0 ] || { adm_fail "a key nobody accepted ran a member's command"; return 1; }
  printf '%s' "$out" | grep -q "Your request is registered" || { adm_fail "the newcomer was not told its request is registered"; return 1; }
  printf '%s' "$out" | grep -q "Members: " || { adm_fail "the refusal does not name the members to ask"; return 1; }
  short="$(printf '%s\n' "$out" | tr -d '\r' | sed -n 's/.* accept \([A-Za-z0-9+/]\{12\}\)$/\1/p' | head -1)"
  [ -n "$short" ] || { adm_fail "no 'accept <key>' line in the refusal"; return 1; }

  # 3. Nor can it open a session.
  rc=0; out="$(plug_bounded 60 -p "$adm_newcomer" -c curl -s --max-time 10 http://adm-echo:8150/ </dev/null 2>&1)" || rc=$?
  if [ "$(printf '%s\n' "$out" | tr -d '\r' | tail -1)" = admitted ]; then
    adm_fail "a key nobody accepted reached adm-echo"; return 1
  fi
  printf '%s' "$out" | grep -qi "by approval" || { adm_fail "a session of a pending key failed without saying why"; return 1; }

  # 4. The member accepts it, by the key the newcomer was told to send.
  rc=0; out="$(plug_bounded 60 -p "$adm_member" accept "$short" </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  { [ "$rc" = 0 ] && printf '%s' "$out" | grep -q "is a member of"; } || { adm_fail "accept $short exited $rc"; return 1; }

  # 5. The agent restarts and reads both members back from where it keeps them.
  out="$(plug_to "$ip_b" curl -s --max-time 10 "http://chaos:8095/restart-agent?svc=$aagent" 2>&1)" || true
  printf '%s' "$out" | grep -q restarting || { adm_fail "chaos did not restart $aagent"; return 1; }
  sleep 8 # the old instance gone (a deleted pod leaves its endpoints in a second or two)
  wait_agent "$ip_b" "$aport" || { out=""; adm_fail "$aagent did not come back after the restart"; return 1; }
  members_after_restart() {
    plug_bounded 60 -p "$adm_member" members </dev/null 2>&1 | grep -c " since "
  }
  if ! retry_until 3 5 2 members_after_restart; then
    out="$(plug_bounded 60 -p "$adm_member" members </dev/null 2>&1)"
    adm_fail "after the restart the agent listed ${ru_out:-no} member(s), want 2"; return 1
  fi

  # 6. The accepted key reaches a service by name.
  newcomer_reach() {
    plug_bounded 90 -p "$adm_newcomer" -c curl -s --max-time 10 http://adm-echo:8150/ </dev/null 2>/dev/null | tr -d '\r' | tail -1
  }
  if ! retry_until 3 5 admitted newcomer_reach; then
    out="$(plug_bounded 90 -p "$adm_newcomer" -c curl -s --max-time 10 http://adm-echo:8150/ </dev/null 2>&1)"
    adm_fail "the accepted key did not reach adm-echo (got '${ru_out:-nothing}')"; return 1
  fi

  # 7. The member revokes it.
  rc=0; out="$(plug_bounded 60 -p "$adm_member" revoke "$short" </dev/null 2>&1)" || rc=$?
  printf '%s\n' "$out" | sed 's/^/    /'
  { [ "$rc" = 0 ] && printf '%s' "$out" | grep -q "is no longer a member"; } || { adm_fail "revoke $short exited $rc"; return 1; }

  # 8. And the key is refused again. On macOS and Windows the daemon still held
  # the newcomer's tunnel from step 6: an agent that did not cut it would serve
  # this session over it, which is the answer that fails here.
  local i refused=""
  for i in 1 2 3 4; do
    rc=0; out="$(plug_bounded 60 -p "$adm_newcomer" -c curl -s --max-time 10 http://adm-echo:8150/ </dev/null 2>&1)" || rc=$?
    if [ "$(printf '%s\n' "$out" | tr -d '\r' | tail -1)" = admitted ]; then
      adm_fail "a revoked key still reached adm-echo (attempt $i)"; return 1
    fi
    printf '%s' "$out" | grep -qi "by approval" && { refused=1; break; }
    sleep 5
  done
  [ -n "$refused" ] || { adm_fail "the revoked key's session failed without the agent's refusal"; return 1; }
  out="$(plug_bounded 60 -p "$adm_member" requests </dev/null 2>&1)" || true
  printf '%s' "$out" | grep -qF "$short" || { adm_fail "the revoked key is not back among the requests"; return 1; }

  echo "admission OK - first member, request, accept, restart, session, revoke, refused again"
  sum "**admission by approval (invite, accept, restart, revoke)** ✅"; return 0
}
