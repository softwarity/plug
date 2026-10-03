#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# mount. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# The live mount: a workload's volumes on the runner, read AND written, with
# nothing installed. One workload per leg (vol-<os>: busybox serving its
# volume over http, VOL_DIR=/data in its environment), so three legs mount
# three volumes at once. Two scenarios on EVERY OS:
#   AUTOMATIC  -c --env-of vol-<os>, no flag: the volume is mounted on its
#              own and $VOL_DIR is repointed at it (a session directory on
#              macOS/Linux, a drive letter on Windows) - the process finds its
#              data where its environment says.
#   EXPLICIT   --mount vol-<os>:/data:<where>, at a place of our choosing (a
#              directory; on Windows the drive letter Y:).
# Each reads the workload's seed through the mount and writes a file through
# it; the assertion that proves the bytes are in the volume is the one made
# through the CLUSTER: the workload serves what was written, fetched by name
# through plug - which holds whatever the runner's shell may read. After the
# sessions nothing of plug's may stay mounted.
do_mount() {
  echo "=== live mount ==="
  local vname="$vol_name" vport="$vol_port" mp
  if [ "$os" = win ]; then
    mp="Y:"
  else
    mp="${RUNNER_TEMP:-/tmp}/plug-vol-$leg"; rm -rf "$mp"; mkdir -p "$mp"
  fi
  still_mounted() { # anything of plug's left mounted, printed
    if [ "$os" = win ]; then net use 2>/dev/null | grep -i "plug-mnt"; else mount | grep "plug-vol-"; fi
  }
  vol_served() { plug curl -s --max-time 10 "http://$vname:$vport/$1" 2>/dev/null | tr -d '\r'; }
  served() { # $1 = file, $2 = want: the workload serves the file written through the mount
    retry_until 6 2 "$2" vol_served "$1" && return 0
    echo "--- mount FAIL - the workload does not serve $1 written through the mount (got '${ru_out:-nothing}', want '$2')"
    return 1
  }
  local out ok=0 tag
  : > /tmp/mount.err
  # AUTOMATIC: no flag; $VOL_DIR arrives repointed at the live mount.
  tag="auto-by-$leg-$$"
  out="$(perl -e 'alarm 150; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" -c --env-of "$vname" \
    bash -c 'echo "VOL_DIR=$VOL_DIR"; cat "$VOL_DIR/seed.txt" && echo && printf "%s" "'"$tag"'" > "$VOL_DIR/from-auto.txt" && echo WROTE' 2>>/tmp/mount.err | tr -d '\r')"
  local vd; vd="$(echo "$out" | grep "^VOL_DIR=" | head -1 | cut -d= -f2-)"
  if [ -z "$vd" ] || [ "$vd" = "/data" ]; then echo "--- mount FAIL - VOL_DIR was not repointed at a live mount (got '${vd:-nothing}')"; ok=1; fi
  echo "$out" | grep -q "seed-from-$vname" || { echo "--- mount FAIL - automatic: the seed was not read through \$VOL_DIR ($vd)"; ok=1; }
  echo "$out" | grep -q "WROTE" || { echo "--- mount FAIL - automatic: writing through \$VOL_DIR ($vd) failed"; ok=1; }
  served from-auto.txt "$tag" || ok=1
  # EXPLICIT: at a place of our choosing.
  tag="explicit-by-$leg-$$"
  out="$(perl -e 'alarm 150; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$port" -c --mount "$vname:/data:$mp" \
    bash -c "cat '$mp/seed.txt' && echo && printf '%s' '$tag' > '$mp/from-plug.txt' && echo WROTE" 2>>/tmp/mount.err | tr -d '\r')"
  echo "$out" | grep -q "seed-from-$vname" || { echo "--- mount FAIL - explicit: the seed was not read through the mount at $mp (got: ${out:-nothing})"; ok=1; }
  echo "$out" | grep -q "WROTE" || { echo "--- mount FAIL - explicit: writing through the mount at $mp failed"; ok=1; }
  served from-plug.txt "$tag" || ok=1
  if left="$(still_mounted)" && [ -n "$left" ]; then
    echo "--- mount FAIL - still mounted after the sessions: $left"; ok=1
  fi
  if [ "$ok" -ne 0 ]; then
    echo "--- plug said:"; cat /tmp/mount.err; sum "**live mount** ❌"; return 1
  fi
  echo "mount OK - automatic (\$VOL_DIR → $vd) and explicit ($mp): seed read, file written, served by the workload, unmounted after"; sum "**live mount** ✅"
}
