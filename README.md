# plug

[![release](https://img.shields.io/github/v/release/softwarity/plug?label=release)](https://github.com/softwarity/plug/releases)
[![docker](https://img.shields.io/docker/v/softwarity/plug?sort=semver&label=docker)](https://hub.docker.com/r/softwarity/plug)
[![docker pulls](https://img.shields.io/docker/pulls/softwarity/plug)](https://hub.docker.com/r/softwarity/plug)
[![license](https://img.shields.io/github/license/softwarity/plug)](LICENSE)
[![arch](https://img.shields.io/badge/arch-amd64%20·%20arm64-brightgreen)](https://hub.docker.com/r/softwarity/plug/tags)
[![CLI](https://img.shields.io/badge/CLI-linux%20·%20macOS%20·%20windows-blue)](https://github.com/softwarity/plug/releases)
[![CI](https://github.com/softwarity/plug/actions/workflows/ci.yml/badge.svg)](https://github.com/softwarity/plug/actions/workflows/ci.yml)

Run a local process as if it were inside your cluster: cluster service names
resolve, and cluster services are reachable — with no code change and no proxy
settings in your app.

```bash
plug npm run start:dev
# your local app now reaches http://my-service:8080 like any workload in the cluster
```

Prefix any command with `plug` and it talks to the cluster by name — Node, the
JVM, Python, Go, curl, gRPC, database drivers, anything. Stop the command and
your machine is exactly as it was.

## What you get

- Reach cluster services by their real names, from your laptop — no port-forwards
  to wire up, no `localhost:PORT` mappings, no `/etc/hosts` edits.
- Works with any language or tool, unchanged — your app's sockets are never touched.
- Runs on Linux, macOS and Windows.
- Several clusters at once, side by side.
- Set up once per cluster, then no sudo or admin for daily use.

## Install

Two pieces: a small agent in the cluster, and the `plug` CLI on each dev machine.

**In the cluster** — add the agent to the stack you want to reach:

```yaml
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports: ["2222:22"]
```

Standalone agent, or Kubernetes: see the [documentation](https://softwarity.github.io/plug/).

**On your machine** — install straight from the cluster, in one line.

Linux and macOS:

```sh
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@<cluster-host> install | sh
```

Windows, from Git Bash:

```bash
ssh -n -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@<cluster-host> install-windows \
  | bash -s -- <cluster-host> 2222
```

The install prepares your machine once — it may ask for your password (or, on
Windows, to run as Administrator) a single time — so that every later `plug` run
needs no privilege. After that you are ready.

## Use

```bash
plug npm run start:dev
plug ./mvnw spring-boot:run
plug curl http://my-service:8080/health
```

The first run asks which cluster to use and remembers it. Reaching another
cluster is just naming it:

```bash
plug -p staging <command>     # asks once, then remembered
```

Everyday commands: `plug ls` (list clusters), `plug test` (check one is
reachable), `plug rn` / `plug rm` (rename / remove), `plug uninstall`,
`plug about`.

## Several clusters at once

Run the same process against two clusters in parallel — each stays isolated:

```bash
plug -p prod    npm run start
plug -p staging npm run start
```

Supported today on Linux and Windows; macOS runs one cluster at a time. See the
[coverage matrix](https://softwarity.github.io/plug/#/coverage) for what works
where.

## Limits

It carries TCP reached by name. UDP, QUIC and ping are not tunnelled (most
clients fall back to TCP), and a hard-coded IPv6 literal is not either — a
service reached by name is always fine.

## Security

There is deliberately no authentication: anyone who can reach the agent's port
gets network access to the cluster it is attached to. Deploy the agent only on
clusters you trust, and never expose its port on an untrusted network. The full
model is in the [documentation](https://softwarity.github.io/plug/#/security).

## Documentation

Everything else — how it works, deployment on Swarm and Kubernetes, profiles and
versions, the security model, and the per-OS coverage matrix:

**https://softwarity.github.io/plug/**

Build from source with `go build -o plug ./cli`. Distribution is from the cluster
only — the agent image is the single source of the CLI, so there is no separate
package to install or keep in sync.
