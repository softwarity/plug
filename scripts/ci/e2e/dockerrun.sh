#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# dockerrun. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
# A CONTAINER as a member of the cluster: `--dockerrun`.
#
# The one cell whose subject is not a process. Everything else here launches a
# program under plug; this launches an IMAGE, which plug cannot serve by
# prefixing docker (the container is made by the daemon, not as plug's child).
# It asserts the same thing the client-only cell does, from inside a container
# nobody modified: httpbin, by name, through the tunnel.
#
# LINUX ONLY, and it says so rather than pretending. The macOS and Windows
# runners have no Linux docker daemon, so the cell reports a skip there instead
# of a pass it did not earn. It stays in the common block all the same: the
# three families must run the same list, and what is being asked is exactly the
# family question - does a container reach a name provisioned by compose, by
# swarm and by k8s alike.
#
# COST, since the legs are a matrix and the slowest sets the wall clock: this
# runs on the three ubuntu legs, which finish 2 to 5 minutes ahead of the macOS
# ones. The image pull it adds fits in that gap. It is also why the cell does
# not pull anything on the platforms that would only skip it.
do_dockerrun() {
  echo "=== a container as a member of the cluster (--dockerrun) ==="
  if [ "$os" != linux ]; then
    echo "not Linux: no docker daemon for a Linux container here, skipped"
    sum "**container member (--dockerrun)** ⏭️ (Linux only)"; return 0
  fi
  if ! docker info >/dev/null 2>&1; then
    echo "--- FAIL: no usable docker daemon on a Linux leg, where there must be one"
    sum "**container member (--dockerrun)** ❌"; return 1
  fi
  if [ -z "${AGENT_IMAGE:-}" ]; then
    echo "--- FAIL: AGENT_IMAGE is unset, so the sidecar has no plug image to run"
    sum "**container member (--dockerrun)** ❌"; return 1
  fi

  # The sidecar runs THIS build's client, not a published release: the flavour
  # and the version have to match the agent it dials, and AGENT_IMAGE is the
  # image this very run built and the cluster is already running.
  local dr_code dr_err=/tmp/dockerrun.err
  dr_code="$(PLUG_DOCKER_IMAGE="$AGENT_IMAGE" perl -e 'alarm 180; exec @ARGV or exit 127' \
    "$PLUG" --host "$ip" --port "$port" -c --dockerrun \
    docker run --rm curlimages/curl:8.11.1 \
      -sS --max-time 20 -o /dev/null -w '%{http_code}' http://httpbin:8080/get \
    2>"$dr_err" | tr -d '\r' | tail -1)"

  if [ "$dr_code" = "200" ]; then
    echo "container OK - an unmodified image reached httpbin by name, through the sidecar's tunnel"
    # And the workload's VOLUME, in the container at its exact cluster path:
    # mounted on this host as for a process, handed over with -v. The file the
    # container writes is served back by the workload, by name.
    local vtag="container-by-$leg-$$" vout
    vout="$(PLUG_DOCKER_IMAGE="$AGENT_IMAGE" perl -e 'alarm 180; exec @ARGV or exit 127' \
      "$PLUG" --host "$ip" --port "$port" -c --env-of vol-linux --dockerrun \
      docker run --rm busybox:1.37 sh -c "cat /data/seed.txt && echo && printf '%s' '$vtag' > /data/from-container.txt && echo WROTE" \
      2>>"$dr_err" | tr -d '\r')"
    local vgot=""
    vol_served() { plug curl -s --max-time 10 http://vol-linux:8120/from-container.txt 2>/dev/null | tr -d '\r'; }
    retry_until 6 2 "$vtag" vol_served; vgot="$ru_out"
    if echo "$vout" | grep -q "seed-from-vol-linux" && echo "$vout" | grep -q WROTE && [ "$vgot" = "$vtag" ]; then
      echo "container OK - the workload's volume was at /data in the container, read and written, served back by the workload"
      sum "**container member (--dockerrun)** ✅"
      return 0
    fi
    echo "--- container FAIL - the volume did not reach the container (container said: ${vout:-nothing}; workload serves '${vgot:-nothing}')"
    echo "    plug said: $(tr -d '\r' < "$dr_err" | tail -8 | tr '\n' ' ')"
    sum "**container member (--dockerrun)** ❌"
    return 1
  fi
  echo "--- container FAIL - got '${dr_code:-nothing}' (want 200)"
  echo "    plug said: $(tr -d '\r' < "$dr_err" | tail -5 | tr '\n' ' ')"
  # The three ways this cell fails, and they are not the same bug. Naming them
  # is the difference between a red square and a place to look.
  case "$(cat "$dr_err" 2>/dev/null)" in
    *"never reported a tunnel"*)
      echo "    the sidecar could not reach $ip:$port FROM A CONTAINER. The leg itself can,"
      echo "    over the tailnet, so this is docker's bridge not carrying that route - a real"
      echo "    limitation of --dockerrun behind a VPN, not a harness fault." ;;
    *"Unable to find image"*|*"not found"*)
      echo "    the sidecar image $AGENT_IMAGE could not be pulled (rate limit, or never published)." ;;
    *"conflicting options"*)
      echo "    docker refused our injected flags against the ones in the command above." ;;
  esac
  sum "**container member (--dockerrun)** ❌"
  return 1
}
