# plug

[![release](https://img.shields.io/github/v/release/softwarity/plug?label=release)](https://github.com/softwarity/plug/releases)
[![docker](https://img.shields.io/docker/v/softwarity/plug?sort=semver&label=docker)](https://hub.docker.com/r/softwarity/plug)
[![license](https://img.shields.io/badge/license-FSL--1.1-blue)](LICENSE)
[![CLI](https://img.shields.io/badge/CLI-linux%20·%20macOS%20·%20windows-blue)](https://github.com/softwarity/plug/releases)
[![CI](https://github.com/softwarity/plug/actions/workflows/ci.yml/badge.svg)](https://github.com/softwarity/plug/actions/workflows/ci.yml)

**plug runs a local process as a member of your cluster: it resolves the
cluster's service names, reaches its services, and is reachable under a name of
its own, with no change to the application.**

A process on your machine cannot see the cluster it is written for. Reaching
it means port-forwards, `localhost` mappings and per-environment URLs, and
nothing in the cluster can call it back. plug removes that gap for the life of
one command.

```bash
plug -s my-app:8080:3000 npm run start:dev
# the dev server reaches cluster services by name (http://orders:8080, mongodb:27017)
# and the cluster reaches it as my-app:8080, forwarded to its local :3000
```

> **Before you deploy it.** By default the agent lets in whoever reaches its
> port. On Docker, Compose and Swarm it needs the Docker socket, which is root
> on the host. The CLI installs over SSH from the agent. Use plug on dev or
> staging clusters you trust, never on a public network.
> [Details](#security).

## What you get

- Cluster services by their real names, from your machine: no port-forward, no
  `/etc/hosts` edit, no proxy setting.
- A name of your own in the cluster: workloads call `my-app:8080` and reach your
  process. A deployed service with that name is set aside for the session and
  restored when it ends.
- On that takeover, the workload's environment and volumes on your machine.
- Any language or tool, unchanged: plug works at the IP layer, not in your
  process.
- Linux, macOS and Windows; several clusters at once.
- A privilege granted once at install; no sudo or admin for daily use.

## Supported platforms

| Cluster | Agent deployment | How plug publishes your name |
|---|---|---|
| Docker Compose | a service in your stack | a signpost container, through the Docker socket |
| Docker Swarm | a service in your stack, on a manager | a signpost service, through the Docker socket |
| Kubernetes | [`deploy/plug-k8s.yaml`](deploy/plug-k8s.yaml) | a Service, through a namespace-scoped Role |
| OpenShift / OKD | the same manifest, `restricted-v2` SCC | a Service, through the same Role |

Compose, Swarm and Kubernetes run end to end in CI on every push, from Linux,
macOS and Windows clients; OpenShift runs on MicroShift built from OKD. What is
proven where, and what is not yet: the
[coverage matrix](https://plug.softwarity.dev/coverage/).

## Install

Two pieces: an agent in the cluster, and the `plug` CLI on each machine.

**In the cluster.** On Compose or Swarm, add the agent to the stack you want
to reach:

```yaml
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports: ["2222:22"]
    # required - the agent creates your -s name through it (see below)
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    # Swarm only, for -s: the signpost is a service, so run the agent on a
    # manager (any single-node swarm node IS a manager) as a single replica.
    # Ignored by plain Compose.
    deploy:
      replicas: 1
      placement:
        constraints: [node.role == manager]
```

On Kubernetes and OpenShift, apply [`deploy/plug-k8s.yaml`](deploy/plug-k8s.yaml).
Every platform, step by step: [Install](https://plug.softwarity.dev/install/).

**On your machine.** Install from the cluster, in one line.

Linux and macOS:

```sh
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@<cluster-host> install | sh
```

Windows, from Git Bash:

```bash
cluster=<cluster-host>
ssh -n -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@$cluster install-windows \
  | bash -s -- $cluster 2222
```

The install may ask for your password (or, on Windows, to run as
Administrator), once. The cluster is saved as a profile named after its host; add a name to
the command to choose your own (`install my-cluster`, or
`bash -s -- $cluster 2222 my-cluster` on Windows).

Then:

```bash
plug -s my-app:8080:3000 npm run start:dev   # a service the cluster can call
plug -c npm test                             # a pure client of the cluster
```

From an empty cluster to a first run:
[Getting started](https://plug.softwarity.dev/getting-started/).

## Limits

- TCP to a name. UDP, QUIC and ping are not carried, nor a hard-coded IPv6
  literal.
- One process serves a name at a time: a second session asking for a name in
  use is refused, and told where it is held from.
- Once in, every session can do the same things. Per-person rights need a
  gateway that embeds the agent, such as Meerkat (below).

## Security

- **Who gets in.** Open by default: the agent accepts whoever reaches its port,
  with a key shipped in every plug binary, which proves the software and not a
  person. With `PLUG_ADMISSION=invite`, only the keys a member accepted get in.
  Through a gateway that embeds the agent, each developer is a named identity.
- **What a session reaches.** The networks the agent is attached to; for any
  workload in reach, its environment, its mounted secret and config files, its
  volumes, and its name. Attach the agent only where developers need it, never
  to production workloads.
- **The Docker socket** (Docker, Compose, Swarm) is root on the host: mount it
  only on a cluster you trust. Kubernetes and OpenShift use a namespace-scoped
  Role instead.
- **The install** runs a script served by the agent over SSH, without checking
  the agent's host key; later connections pin it. The CLI binaries are signed,
  and plug verifies the signature before granting itself privilege.

Appropriate for dev and staging clusters on a network you trust. Never expose
the agent's port publicly. The full model:
[Security](https://plug.softwarity.dev/security/). Maintainers: signing key and
rotation in [RELEASING.md](RELEASING.md).

## License

plug is [FSL-1.1-Apache-2.0](LICENSE) - free for any purpose, including
building it into your own product, self-hosting it, or using it internally at
a company. The one thing it doesn't permit is a **competing use**: offering
plug itself, or a product/service that substitutes for it (e.g. a rival
hosted-gateway offering), to others. It converts to Apache-2.0 two years after
each release. Want to do exactly that anyway? A commercial license is
available - contact **[francois@hhdev.fr](mailto:francois@hhdev.fr)**.

Third-party components are listed in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).

## Documentation

**https://plug.softwarity.dev**: getting started, install per platform, how it
works, the CLI, security, the comparison with other tools, troubleshooting.

Build from source with `go build -o plug ./cli`. The agent image is the only
source of the CLI: developers install it from their cluster.

plug is free and has no paid tier. To support its development:
[GitHub Sponsors](https://github.com/sponsors/softwarity).

[Meerkat](https://softwarity.github.io/meerkat/), an app gateway by the same
team, embeds the plug agent and ties each session to a named developer.
