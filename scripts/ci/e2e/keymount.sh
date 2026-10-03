#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# keymount. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
# do_keymount exercises the one branch of --dockerrun that NO cluster in this
# repository could reach: the profile's OWN private key, mounted into the sidecar.
#
# flavour.go keeps `keygen` to the hosted build, so a standalone cluster checks no
# personal key, cfg.key is empty on every cluster this CI stands up (the dockerrun
# cell above included), and those four lines were written, compiled, shipped and
# never executed. cli/dockerrun_test.go asserts the argv they build; what no test
# had was a session that actually authenticates with that key.
#
# So this cell builds the cluster the repository lacks. An agent of its own, whose
# authorized_keys holds ONE throwaway key and NOT the key built into plug. That
# inversion is the whole point: the built-in key is refused here, so a tunnel that
# comes up could only have been carried by the mounted key. The control at the end
# proves that refusal is real rather than an agent taking whatever it is offered.
#
# CI only, deliberately. An agent sweeps orphaned plug signposts on the daemon it
# is handed, at boot (dockerGC in agent/main.go). That is right for the dedicated
# daemon of a cluster and wrong for a workstation, where a real plug deployment
# may be running and its live signposts are not this cell's to consider. A
# runner's daemon is empty and thrown away with the runner. To run this cell on a
# developer machine, give the agent a docker-in-docker daemon instead of the real
# one: everything else here works unchanged.
do_keymount() {
  echo "=== the profile's own key, mounted into the sidecar (--dockerrun) ==="
  if [ "$os" != linux ]; then
    echo "not Linux: no docker daemon for a Linux container here, skipped"
    sum "**key mount (--dockerrun)** ⏭️ (Linux only)"; return 0
  fi
  if [ -z "${GITHUB_ACTIONS:-}" ]; then
    echo "not a CI runner: this cell hands an agent THIS machine's docker daemon, and an agent"
    echo "sweeps plug signposts at boot. Give it a dind daemon instead, or leave it alone."
    sum "**key mount (--dockerrun)** ⏭️ (CI only)"; return 0
  fi
  if [ -z "${AGENT_IMAGE:-}" ]; then
    echo "--- FAIL: AGENT_IMAGE is unset, so there is no plug image to run as agent or as sidecar"
    sum "**key mount (--dockerrun)** ❌"; return 1
  fi

  km_dir="$(mktemp -d)"
  km_net=plug-keymount
  km_clean() {
    docker rm -f plug-keymount-agent plug-keymount-httpbin >/dev/null 2>&1
    docker network rm "$km_net" >/dev/null 2>&1
    rm -rf "$km_dir"
  }

  # A throwaway pair, born in a temp directory and dying with this cell. It is
  # enrolled nowhere else, and no key belonging to this repository is involved.
  if ! ssh-keygen -t ed25519 -N '' -q -C plug-keymount-throwaway -f "$km_dir/id"; then
    echo "--- FAIL: cannot generate a throwaway key pair"
    sum "**key mount (--dockerrun)** ❌"; km_clean; return 1
  fi
  cp "$km_dir/id.pub" "$km_dir/authorized_keys"

  docker network create "$km_net" >/dev/null 2>&1
  # The only key this agent will accept. The one built into plug is deliberately
  # absent from that file: it is what makes the result below mean anything.
  docker run -d --name plug-keymount-agent --network "$km_net" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$km_dir/authorized_keys:/var/lib/plug/authorized_keys:ro" \
    "$AGENT_IMAGE" >/dev/null 2>&1
  docker run -d --name plug-keymount-httpbin --network "$km_net" --network-alias httpbin \
    mccutchen/go-httpbin:v2.15.0 >/dev/null 2>&1
  km_up="" km_n=0
  while [ "$km_n" -lt 30 ]; do
    if docker logs plug-keymount-agent 2>&1 | grep -q "ready (v"; then km_up=1; break; fi
    km_n=$((km_n + 1)); sleep 1
  done
  if [ -z "$km_up" ]; then
    echo "--- FAIL: the throwaway agent never came up. It said:"
    docker logs plug-keymount-agent 2>&1 | tail -10
    sum "**key mount (--dockerrun)** ❌"; km_clean; return 1
  fi

  # An isolated HOME. A profile is the only way to name a key (no flag carries
  # one), and writing profiles here must not disturb the one the other cells use.
  mkdir -p "$km_dir/home/.plug"
  printf 'host = plug-keymount-agent\nport = 22\nkey = %s\n' "$km_dir/id" > "$km_dir/home/.plug/keyed.conf"
  printf 'host = plug-keymount-agent\nport = 22\n' > "$km_dir/home/.plug/bare.conf"

  km_code="$(HOME="$km_dir/home" PLUG_DOCKER_IMAGE="$AGENT_IMAGE" PLUG_DOCKER_NETWORK="$km_net" \
    perl -e 'alarm 180; exec @ARGV or exit 127' \
    "$PLUG" -p keyed -c --dockerrun \
    docker run --rm curlimages/curl:8.11.1 \
      -sS --max-time 20 -o /dev/null -w '%{http_code}' http://httpbin:8080/get \
    2>"$km_dir/err" | tr -d '\r' | tail -1)"
  if [ "$km_code" != "200" ]; then
    echo "--- FAIL: with its key mounted, the container got '${km_code:-nothing}' (want 200)"
    echo "    plug said: $(tr -d '\r' < "$km_dir/err" | tail -5 | tr '\n' ' ')"
    sum "**key mount (--dockerrun)** ❌"; km_clean; return 1
  fi
  echo "keyed OK - the sidecar authenticated with the key it had mounted, and the container reached httpbin by name"

  # The control, and the reason this cell is not circular: take the key line out
  # and the very same command must be REFUSED. Without it, an agent that accepted
  # anything would answer 200 just the same and this cell would assert nothing.
  HOME="$km_dir/home" PLUG_DOCKER_IMAGE="$AGENT_IMAGE" PLUG_DOCKER_NETWORK="$km_net" \
    perl -e 'alarm 180; exec @ARGV or exit 127' \
    "$PLUG" -p bare -c --dockerrun \
    docker run --rm curlimages/curl:8.11.1 -sS --max-time 20 -o /dev/null http://httpbin:8080/get \
    >/dev/null 2>"$km_dir/bare.err"
  if grep -q "refused every key" "$km_dir/bare.err"; then
    echo "control OK - the same profile without its key line is refused, so the 200 above came from the mount"
    sum "**key mount (--dockerrun)** ✅"
    km_clean; return 0
  fi
  echo "--- FAIL: the profile WITHOUT a key was not refused, so nothing above proves the mount worked"
  echo "    it said: $(tr -d '\r' < "$km_dir/bare.err" | tail -5 | tr '\n' ' ')"
  sum "**key mount (--dockerrun)** ❌"; km_clean; return 1
}
