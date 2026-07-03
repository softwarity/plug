# plug

[![release](https://img.shields.io/github/v/release/softwarity/plug?label=release)](https://github.com/softwarity/plug/releases)
[![docker](https://img.shields.io/docker/v/softwarity/plug-agent?sort=semver&label=docker)](https://hub.docker.com/r/softwarity/plug-agent)
[![docker pulls](https://img.shields.io/docker/pulls/softwarity/plug-agent)](https://hub.docker.com/r/softwarity/plug-agent)
[![license](https://img.shields.io/github/license/softwarity/plug)](LICENSE)
[![arch](https://img.shields.io/badge/arch-amd64%20·%20arm64-brightgreen)](https://hub.docker.com/r/softwarity/plug-agent/tags)
[![CLI](https://img.shields.io/badge/CLI-linux%20·%20macOS%20·%20windows-blue)](https://github.com/softwarity/plug/releases)
[![CI](https://github.com/softwarity/plug/actions/workflows/ci.yml/badge.svg)](https://github.com/softwarity/plug/actions/workflows/ci.yml)
[![docs](https://img.shields.io/badge/docs-softwarity.github.io%2Fplug-8A2BE2)](https://softwarity.github.io/plug/)

Run a local process **as if it were inside your Docker Swarm cluster**: cluster
DNS names resolve, cluster services are reachable — no code change, no proxy
config in your app.

📖 **Full documentation: [softwarity.github.io/plug](https://softwarity.github.io/plug/)**

```bash
plug npm run start:dev
# your local NestJS/Spring/Quarkus now resolves and reaches
# http://my-service:8080 like any container in the stack
```

## How it works

```
┌─ your laptop ──────────────┐         ┌─ swarm cluster ───────────────┐
│  plug <cmd>                │         │  plug-agent (alpine + sshd)   │
│   ├─ discovers subnets ────┼──ssh────┼─→ attached to overlay nets    │
│   ├─ sshuttle tunnel ──────┼──:2222──┼─→ relays traffic + DNS        │
│   └─ runs <cmd>            │         │   (resolver 127.0.0.11)       │
└────────────────────────────┘         └───────────────────────────────┘
```

`plug` starts an [sshuttle](https://github.com/sshuttle/sshuttle) tunnel to a
tiny agent container deployed in the cluster, auto-discovers the overlay
subnets from the agent itself, routes them (plus DNS) through the tunnel, runs
your command, and tears everything down when it exits.

## Setup

**Once, on the cluster** — add the agent to your application stack; it joins
the stack's network automatically:

```yaml
services:
  plug-agent:
    image: docker.io/softwarity/plug-agent:latest
    ports:
      - "2222:22"
```

Alternative — one standalone agent covering several stacks:
[deploy/plug-stack.yml](deploy/plug-stack.yml) lists their overlay networks
explicitly (`docker stack deploy -c plug-stack.yml plug`).

**On each dev machine:**

```bash
brew install sshuttle          # macOS; linux: apt/dnf install sshuttle
```

Then get the `plug` binary **from the cluster itself** — the agent serves it on
the same port `2222`, and `uname` picks your platform, so there is nothing to
choose and no GitHub access needed:

```bash
ssh -p 2222 get@<cluster-host> $(uname -s)-$(uname -m) > plug && chmod +x plug
sudo mv plug /usr/local/bin/
```

The `get` user is passwordless and locked (via `ForceCommand`) to a single
"hand me a binary" command — see [Security model](https://softwarity.github.io/plug/#/security).
Prefer GitHub? The same binaries are attached to every
[release](https://github.com/softwarity/plug/releases). Build from source with
`make cli && make install`.

> **Windows**: a `windows-amd64` binary is published, but sshuttle has no
> native Windows support — run plug inside WSL2 (with the linux binary)
> instead.

### Staying in sync

On connect, plug compares itself to the agent and **upgrades itself when the
cluster ships a newer version** (over the same SSH channel — no extra port).
With several clusters on different versions it never downgrades: the CLI
converges to the newest and stays compatible with older agents of the same
major. `plug upgrade` syncs on demand; `auto-upgrade = false` (profile) or
`PLUG_AUTO_UPGRADE=0` opts out.

## Usage

```bash
plug npm run start:dev
plug ./mvnw spring-boot:run
```

Profiles live in `~/.plug/*.conf` and are picked automatically:

- **no profile** → a short wizard asks for a name, the cluster host and the
  agent port (default 2222), then saves and uses it
- **one profile** → used as is
- **several profiles** → interactive selection, or pick one with `-p staging`

`plug init` runs the same wizard on demand (e.g. to add a second cluster).
A profile is a plain file you can also edit by hand:

```ini
# ~/.plug/staging.conf
host = swarm-node.example.com
port = 2222
# subnets = 10.0.9.0/24        # optional, skips auto-discovery
```

`--host`/`--port` (or `$PLUG_HOST`/`$PLUG_PORT`) bypass profiles entirely.
sudo will prompt once per session (sshuttle needs it for local packet
redirection).

## Security model — read this

**There is deliberately no authentication.** The SSH keypair is embedded in
this repository and in every `plug` binary; it is a transport detail, not a
secret. Anyone who can reach the agent port has full network access to the
attached overlay networks.

Only deploy the agent on clusters and networks you already trust (internal dev
clusters). Never publish port 2222 on an untrusted network.

## Roadmap

- [x] CLI served from the cluster (`get` user) + self-upgrade with a skew policy
- [ ] Kubernetes transport (agent pod + `kubectl exec`)
- [ ] Embed the agent into an API gateway (dynamic enable/disable), exposing the
      same download/upgrade surface it already speaks
- [ ] Homebrew tap
