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
    # optional — only to serve a local port back to the cluster (plug -s), see below
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

The socket line is **opt-in**: leave it out and plug still does the forward
direction (`plug <cmd>` reaching cluster services). You only need it to serve a
local port *to* the cluster — see [below](#serve-a-local-service-to-the-cluster).

Standalone agent, or Kubernetes: see the [documentation](https://softwarity.github.io/plug/).

**On your machine** — install straight from the cluster, in one line.

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

Supported on all three OSes — proven simultaneously in CI on Linux, macOS and
Windows. See the [coverage matrix](https://softwarity.github.io/plug/#/coverage)
for the details.

## Serve a local service to the cluster

The session also works in reverse: `-s` makes a local port reachable from
inside the cluster, under a cluster DNS name, for the lifetime of the session.

```bash
plug -s service1:8081:4200 npm run start:dev
```

Any workload calling `http://service1:8081` inside the cluster now lands on
your machine's `:4200` — **no name pre-declared, no redeploy**. The agent
creates the name on the fly, which it does per engine:

- **Docker / Compose** — mount the Docker socket on the agent (opt-in). Each
  `-s` spins up a tiny *signpost* container carrying the DNS alias, removed with
  the session.
- **Swarm** — same socket; the signpost is a Swarm *service*, which joins the
  stack's overlay whether or not it is `attachable`, so **no network change**.
  The agent just needs to run on a **manager** node (to create services).
- **Kubernetes** — the bundled [manifest](deploy/plug-k8s.yaml) already grants a
  Services-only RBAC role, so `-s` creates and deletes the backing Service itself.
- **Neither** — the agent answers *static*: pre-declare the name (a network
  alias, a Service) and `-s` works the same, minus the auto-provisioning.

```yaml
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports: ["2222:22"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock   # opt-in: dynamic -s names
```

The Docker socket is root on the host — mount it only on a cluster you trust
(the same trust plug's no-auth transport already assumes). plug verifies the
full path at startup so a missing name fails loud, and the port closes with the
session.

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
