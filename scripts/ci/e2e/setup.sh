#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# setup: the real user flow - install plug FROM the cluster (the installer grants
# the privilege the real way: setcap / setuid helper / SCM SYSTEM service), build
# the four language clients, and record PLUG/ip/built for the cells after it.
# Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
# why_no_cluster prints the ONE fact that tells the two causes apart: was the
# agent image this cluster runs even published yet?
#
# A ten-legged run said "cluster never became reachable" ten times over, and the
# cause was neither the clusters nor the legs - the amd64 image build had taken
# 18m30 instead of its usual 2m30, so every cluster gave up pulling before it
# existed. From a leg the two states are identical, and reading them the wrong
# way round costs an hour of looking at the wrong thing.
why_no_cluster() {
  [ -n "${AGENT_IMAGE:-}" ] || return 0
  wn_repo="${AGENT_IMAGE#*/}"; wn_repo="${wn_repo%%:*}"; wn_tag="${AGENT_IMAGE##*:}"
  wn_tok="$(curl -s --max-time 20 "https://auth.docker.io/token?service=registry.docker.io&scope=repository:${wn_repo}:pull" \
            | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
  [ -n "$wn_tok" ] || { echo "    (could not ask the registry - no verdict on the image)" >&2; return 0; }
  wn_code="$(curl -s --max-time 20 -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $wn_tok" \
             -H "Accept: application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json" \
             "https://registry-1.docker.io/v2/${wn_repo}/manifests/${wn_tag}")"
  if [ "$wn_code" = 200 ]; then
    echo "    $AGENT_IMAGE IS published - so the image is not the reason; look at the cluster run" >&2
  else
    echo "    $AGENT_IMAGE is NOT published yet (registry said $wn_code) - the clusters had nothing to pull." >&2
    echo "    This is the image build being slow, not the cluster or this leg. Re-run once it is up." >&2
  fi
}

do_setup() {
  echo "=== wait for cluster A ($peer:$port) ==="
  ip="$(wait_cluster "$peer")" || { echo "cluster $peer never became reachable" >&2; why_no_cluster; exit 1; }
  echo "cluster A reachable at $ip:$port"

  echo "=== install plug from the cluster (real user flow) ==="
  case "$os" in
    win)
      ssh -n $SSH_OPTS "get@$ip" install-windows | bash -s -- "$ip" "$port" || { echo "windows install failed" >&2; exit 1; }
      PLUG="$(cygpath "$LOCALAPPDATA")/Programs/plug/plug.exe"
      ;;
    *)
      # No -n: the unix installer reads the cluster host off this live ssh command.
      ssh $SSH_OPTS "get@$ip" install </dev/null | sh || { echo "install failed" >&2; exit 1; }
      PLUG=""
      for c in "$(command -v plug 2>/dev/null || true)" "$HOME/.local/bin/plug" /usr/local/bin/plug; do
        [ -n "$c" ] && [ -x "$c" ] && PLUG="$c" && break
      done
      ;;
  esac
  [ -n "$PLUG" ] && [ -x "$PLUG" ] || { echo "plug not found after install" >&2; exit 1; }
  echo "installed: $PLUG"
  "$PLUG" test --host "$ip" --port "$port" || { echo "installed plug cannot reach cluster A" >&2; exit 1; }

  echo "=== build clients ==="
  # Each one takes what the shared build already produced, and falls back to
  # building it. Only python has nothing to take: its wheels are compiled.
  build_go()   { take_prebuilt eclient "$clients/go/eclient$ext" || ( cd "$clients/go" && go build -o "eclient$ext" . ); }
  build_java() {
    [ -n "$prebuilt" ] && [ -f "$prebuilt/client.jar" ] && {
      mkdir -p "$clients/java/target" && cp "$prebuilt/client.jar" "$clients/java/target/client.jar"; return $?; }
    ( cd "$clients/java" && mvn -e -B package ) # no -q: surface the goal on failure
  }
  build_node() {
    [ -n "$prebuilt" ] && [ -f "$prebuilt/node_modules.tar.gz" ] && {
      tar -xzf "$prebuilt/node_modules.tar.gz" -C "$clients/node"; return $?; }
    ( cd "$clients/node" && npm install --omit=dev --no-audit --no-fund )
  }
  # --break-system-packages: macOS runners ship a Homebrew Python that refuses a
  # plain `pip install` (externally-managed-environment).
  build_python() { $py -m pip install --quiet --disable-pip-version-check --user --break-system-packages -r "$clients/python/requirements.txt"; }
  built=""
  for l in $LANGS; do
    if "build_$l" >"/tmp/build-$l.log" 2>&1; then
      built="$built $l"; echo "  $l: ok"
    else
      echo "  $l: BUILD FAILED"
      # Surface the real cause first - a blind `tail` often shows only the generic
      # Maven stack-trace epilogue, not the "[ERROR] Failed to execute goal ..." line.
      grep -iE "\[ERROR\]|BUILD FAILURE|Caused by|Exception|error:|invalid target|not supported|release version" \
        "/tmp/build-$l.log" | head -15 | sed 's/^/    | /' || true
      tail -15 "/tmp/build-$l.log" | sed 's/^/    /'
    fi
  done

  # wait_cluster proves the AGENT answers ssh. It says nothing about the
  # SERVICES the cells reach by name - on kind especially, pods are still
  # starting well after the agent is up.
  #
  # That gap was invisible while setup spent 769s building four language
  # clients: everything had long finished settling by the time the first cell
  # ran. Setup takes about a minute now, and `client-only` failed twice in a day
  # with curl code 000 on a cluster that was simply not up yet - a retry inside
  # the cell did not help, because the wait needed is tens of seconds, not five.
  #
  # So wait HERE, once, for the whole run rather than in each early cell. The
  # probe is the same shape as the first assertion that needs it: `-c` reaching
  # httpbin by name. How long it waited is printed on purpose - if that number
  # ever grows, it is the cluster getting slower to come up, and this line is
  # where you would see it rather than a cell failing for a reason of its own.
  echo "=== wait for the cluster's services (an agent answering is not a cluster ready) ==="
  svc_t=0
  while [ "$svc_t" -lt 150 ]; do
    if [ "$(perl -e 'alarm shift @ARGV; exec @ARGV or exit 127' 45 "$PLUG" --host "$ip" --port "$port" -c \
            curl -s --max-time 10 -o /dev/null -w '%{http_code}' http://httpbin:8080/get 2>/dev/null | tr -d '\r' | tail -1)" = 200 ]; then
      echo "services answering after ${svc_t}s"
      break
    fi
    sleep 10; svc_t=$((svc_t + 10))
  done
  [ "$svc_t" -lt 150 ] || echo "--- services still not answering after ${svc_t}s - the cells will say what that costs"

  # The job's own clock, so a LATE cell can work out how long it may safely hang.
  # See CELL_MAX below: a fixed budget protects the cells at the start of a leg
  # and cannot protect the ones at the end, which are the ones that hang.
  #
  # PLUG_JOB_T0 is the job's FIRST step writing `date +%s` into GITHUB_ENV, and
  # it is what counts. The clock used to start HERE, at the end of setup, which
  # is after wait_cluster (up to ten minutes), the install and the wait for the
  # services: the watchdog then believed a late cell had that much more room
  # than the job did, and its alarm rang after GitHub had killed the job, which
  # is the exact bug it exists to fix. The fallback is for a run outside CI.
  { echo "PLUG='$PLUG'"; echo "ip='$ip'"; echo "built='$built'"
    echo "job_started='${PLUG_JOB_T0:-$(date +%s)}'"; } > "$envfile"
  rm -f "$skip_file"
  echo "state → $envfile"
  sum "### plug mesh e2e - $(uname -s)"
  sum "**install** ✅ · clients built:${built:- none}"
}
