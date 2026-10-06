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
  privdrop_profiles
}

# The other side of the same privilege: what plug does with the caller's OWN
# files while it holds it. A launcher up to 2.21.0 wrote profiles as root and
# never handed them back, and the July guard then refused to touch them: plug
# could no longer remove, rename or redefine its own profiles. plug now edits
# by replacing the entry in a directory proven the caller's, never by writing
# into the file (cli/profilestore.go), so a root-owned profile is managed like
# any other and a hard link to a root-only file is neither written nor read.
# The unit tests stand in the ownership; only a real setuid launcher and a real
# root-owned file prove it, hence this cell. In a home of its own: the runner's
# ~/.plug holds the profiles the rest of the leg uses.
privdrop_profiles() {
  echo "=== profiles plug wrote as root: still the caller's to manage ==="
  if [ "$os" = win ]; then
    echo "not measured on Windows: the launcher holds no privilege over the user's files there"
    sum "**root-owned profiles** ⏭️ (no setuid launcher on Windows)"; return 0
  fi
  if [ "$(id -u)" = 0 ] || ! sudo -n true 2>/dev/null; then
    echo "NOT MEASURABLE: needs a non-root caller with sudo to make a root-owned file"
    sum "**root-owned profiles** (not measurable on this runner)"; return 0
  fi
  local h me victim fail="" owner
  h="$(mktemp -d)"; me="$(id -u)"; victim="$(mktemp -u)"
  ownerof() { ls -ldn "$1" 2>/dev/null | awk '{print $3}'; }
  pp() { HOME="$h" plug_bounded 20 "$@"; }

  # 1. No ~/.plug yet: the first profile makes it, and makes it the caller's.
  pp -p first -H first.invalid --port 2222 >/dev/null 2>&1
  owner="$(ownerof "$h/.plug")"
  [ "$owner" = "$me" ] || fail="$fail ~/.plug made as uid '${owner:-none}', not $me;"

  # 2. A root-owned profile, as an old launcher left it: renamed and removed.
  sudo sh -c "printf 'host = old.invalid\nport = 2222\n' > '$h/.plug/legacy.conf' && chown 0 '$h/.plug/legacy.conf' && chmod 600 '$h/.plug/legacy.conf'"
  if [ "$os" = mac ]; then
    # Redefined too, which reads the old text first: only the setuid launcher
    # can, a Linux launcher holds no privilege over files and reads as the caller.
    pp -p legacy -H new.invalid --port 2300 >/dev/null 2>&1
    owner="$(ownerof "$h/.plug/legacy.conf")"
    [ "$owner" = "$me" ] || fail="$fail the redefined profile is uid '${owner:-none}', not $me;"
    grep -q "new.invalid" "$h/.plug/legacy.conf" 2>/dev/null || fail="$fail the profile was not redefined;"
    sudo chown 0 "$h/.plug/legacy.conf"
  fi
  pp rn legacy moved >/dev/null 2>&1
  [ -e "$h/.plug/moved.conf" ] && [ ! -e "$h/.plug/legacy.conf" ] || fail="$fail plug rn refused a root-owned profile;"
  pp rm moved >/dev/null 2>&1
  [ ! -e "$h/.plug/moved.conf" ] || fail="$fail plug rm refused a root-owned profile;"

  # 3. A hard link to a root-only file: defining the profile must not write
  # into it nor copy it out; removing it removes the link only.
  sudo sh -c "printf 'SECRET\n' > '$victim' && chmod 600 '$victim' && ln '$victim' '$h/.plug/linked.conf'"
  pp -p linked -H x.invalid --port 2222 >/dev/null 2>&1
  [ "$(sudo cat "$victim")" = SECRET ] || fail="$fail a hard-linked root file was written into;"
  ! grep -q SECRET "$h/.plug/linked.conf" 2>/dev/null || fail="$fail a hard-linked root file was copied into the profile;"
  pp rm linked >/dev/null 2>&1
  [ "$(sudo cat "$victim")" = SECRET ] || fail="$fail removing the link touched the root file;"

  sudo rm -rf "$h" "$victim"
  if [ -n "$fail" ]; then
    echo "--- root-owned profiles FAIL:$fail"
    sum "**root-owned profiles** FAILED:$fail"; return 1
  fi
  echo "root-owned profiles OK: ~/.plug made as the caller's, a root-owned profile renamed and removed$([ "$os" = mac ] && echo ' and redefined as the caller'"'"'s'), a hard-linked root file left untouched"
  sum "**root-owned profiles managed, hard links untouched** OK"
}
