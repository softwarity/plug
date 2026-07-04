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
┌─ your laptop ──────────────────┐        ┌─ swarm cluster ────────────┐
│  plug <cmd>                    │        │  plug-agent (alpine+sshd)  │
│   ├─ local SOCKS5 proxy ───────┼──ssh───┼─→ direct-tcpip: sshd dials │
│   │   ALL_PROXY → child        │  :2222 │   service:port & resolves  │
│   ├─ per-session port-forwards │        │   names inside the cluster │
│   └─ runs <cmd>                │        │                            │
└────────────────────────────────┘        └────────────────────────────┘
```

`plug` opens a local **SOCKS5 proxy** backed by an SSH tunnel to a tiny agent in
the cluster, and points the child's environment at it (`ALL_PROXY`,
`JAVA_TOOL_OPTIONS`). Cluster names resolve because the proxy hands hostnames to
the agent, which resolves them **inside** the cluster (`socks5h`) — so
`http://my-service:8080` just works, with **no root, no TUN, no daemon**.
Because nothing global is touched, **several sessions to different clusters run
side by side** (compare the same process against two environments at once).

For raw-TCP services whose driver ignores the proxy (AMQP, Kafka…), declare a
**port-forward** (below): plug opens a per-session local port and injects its
address into the child's environment.

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

**On each dev machine** — install straight from the cluster, one line (the
agent's installer downloads the right binary, puts it on your `PATH` and writes
a default profile; no GitHub access needed):

```bash
ssh -p 2222 get@<cluster-host> install | sh -s -- <cluster-host> 2222
```

The `get` user is passwordless and locked (via `ForceCommand`) to a single
"hand me a binary / installer" command — see
[Security model](https://softwarity.github.io/plug/#/security). Prefer GitHub?
The same binaries are attached to every
[release](https://github.com/softwarity/plug/releases). Build from source with
`make cli && make install`.

That's the whole install — **no other dependency, no root**. The binary is a
single static Go executable (~6&nbsp;MB).

### Versions — the launcher model

plug is a small **launcher** (like `nvm`/`rustup`). On each run it asks the
agent which version it speaks and executes *that exact version* from
`~/.plug/versions/`, downloading it once if missing. Each cluster runs its own
matching version, so several clusters on different versions never conflict —
nothing is replaced in place. `plug versions` lists what's cached;
`plug self-update` refreshes the launcher itself (rarely needed).

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
# raw-TCP services whose driver ignores the proxy — plug forwards a local
# port and injects the rewritten address into the child's env:
forward = AMQP_URL=amqp://rabbitmq:5672, MONGO_URL=mongodb://mongodb:27017
```

`--host`/`--port` (or `$PLUG_HOST`/`$PLUG_PORT`) bypass profiles entirely.

### Port-forwards — for drivers that ignore the proxy

HTTP clients and the whole JVM honor the SOCKS proxy, so most things just work.
Some raw-TCP drivers (Node's `amqplib`, some Kafka/Redis clients) don't. Declare
them with `forward = ENV=url`: plug opens a per-session local port to the cluster
service and sets `ENV` to the local address (scheme and credentials preserved).
Your 12-factor app reads its connection string from the env, so no code changes —
and each session gets its own ports, so multiple clusters never collide.

### Multiple clusters at once

Because there is no global state (no system DNS, no `/etc/hosts`, no firewall,
no TUN), you can run the same process against several clusters simultaneously —
each `plug -p <profile> <cmd>` has its own proxy and forward ports:

```bash
plug -p prod    npm run start   # → cluster prod
plug -p staging npm run start   # → cluster staging, in parallel
```

## Security model — read this

**There is deliberately no authentication.** The SSH keypair is embedded in
this repository and in every `plug` binary; it is a transport detail, not a
secret. Anyone who can reach the agent port has full network access to the
attached overlay networks.

Only deploy the agent on clusters and networks you already trust (internal dev
clusters). Never publish port 2222 on an untrusted network.

## Roadmap

- [x] Rootless SOCKS5 data path + per-session port-forwards (multi-cluster)
- [x] Install from the cluster (`get` user) + launcher with per-cluster versions
- [ ] Kubernetes transport (agent pod + `kubectl exec`)
- [ ] Embed the agent into an API gateway (dynamic enable/disable), exposing the
      same install/version surface it already speaks
- [ ] Homebrew tap
