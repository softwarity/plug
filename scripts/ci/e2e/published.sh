#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2154 # the globals come from lib.sh and the setup state
# published. Sourced by scripts/ci/e2e-matrix.sh after lib.sh.
#
# published: the files the site offers for download start an agent that works.
# scripts/ci/published-agents.sh deployed them on cluster A as a reader would,
# image and port aside, beside an app (e2e/published.app.yml). Against each, a
# session reaches the app by name (-c), and the app reaches a name the session
# serves (-s): the socket is mounted, the network joined, the port published.
#
# From the Linux leg only: what is under test is the file, not the client, and
# every client already runs the chain against the e2e agent. On Kubernetes that
# agent IS the published manifest (k8s-serve.sh), so there is nothing to add.
do_published() {
  if [ "$family" = k8s ]; then
    echo "published: the chain's agent is deploy/plug-k8s.yaml as published (scripts/ci/k8s-serve.sh)"
    return 0
  fi
  if [ "$leg" != linux ]; then
    echo "published: run from the Linux leg"
    return 0
  fi
  if ! helper_bin echo-local; then
    echo "--- published FAIL - echo-local did not build"; sum "**published files** ❌ (build)"; return 1
  fi
  local rc=0
  published_one 2241 18196 docs/snippets/plug-service.yml || rc=1
  if [ "$family" = swarm ]; then
    published_one 2242 18197 deploy/plug-stack.yml || rc=1
  fi
  return "$rc"
}

# published_one <agent port> <local port> <file>
published_one() {
  local aport="$1" lport="$2" file="$3" name="pub-$1" code out pid
  echo "=== published: $file, deployed as published on :$aport ==="
  pub_fail() {
    echo "--- published FAIL ($file) - $1"
    tail -12 "/tmp/published-$aport.out" 2>/dev/null | sed 's/^/    /'
    sum "**published files** ❌ - \`$file\`: $1"
  }
  wait_agent "$ip" "$aport" || { pub_fail "the agent never answered on :$aport"; return 1; }

  # 1. A session reaches the app by name. The prober answers 400 to a fetch
  # with no url: any answer at all is the name resolved and the service reached.
  code="$(perl -e 'alarm 60; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$aport" -c \
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 http://pub-prober:8097/fetch 2>/dev/null | tr -d '\r' | tail -1)"
  [ "$code" = 400 ] || { pub_fail "-c reached pub-prober with HTTP '${code:-nothing}' (want 400)"; return 1; }

  # 2. The app reaches a name the session serves: the agent made the signpost
  # through the socket the file mounts, on the network it joined.
  "$PLUG" --host "$ip" --port "$aport" -s "$name:18195:$lport" \
    "$root/echo-local$ext" -addr "127.0.0.1:$lport" -text "published-ok-$name" -ttl 90s \
    >"/tmp/published-$aport.out" 2>&1 &
  pid=$!
  sleep 8 # arm and verify, as the expose cell waits
  pub_probe() {
    perl -e 'alarm 60; exec @ARGV or exit 127' "$PLUG" --host "$ip" --port "$aport" -c \
      curl -s --max-time 10 "http://pub-prober:8097/fetch?url=http://$name:18195/" 2>/dev/null | tr -d '\r' | tail -1
  }
  retry_until 3 3 "published-ok-$name" pub_probe; out="$ru_out"
  stop_bg "$pid" "the published -s session"
  [ "$out" = "published-ok-$name" ] || { pub_fail "the app fetched '${out:-nothing}' from $name (want published-ok-$name)"; return 1; }

  echo "published OK - $file: -c reached the app by name, the app reached the -s name"
  sum "**published files** ✅ \`$file\`"
}
