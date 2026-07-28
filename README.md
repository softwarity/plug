# plug

[![release](https://img.shields.io/github/v/release/softwarity/plug?label=release)](https://github.com/softwarity/plug/releases)
[![docker](https://img.shields.io/docker/v/softwarity/plug?sort=semver&label=docker)](https://hub.docker.com/r/softwarity/plug)
[![docker pulls](https://img.shields.io/docker/pulls/softwarity/plug)](https://hub.docker.com/r/softwarity/plug)
[![license](https://img.shields.io/badge/license-FSL--1.1-blue)](LICENSE)
[![arch](https://img.shields.io/badge/arch-amd64%20·%20arm64-brightgreen)](https://hub.docker.com/r/softwarity/plug/tags)
[![CLI](https://img.shields.io/badge/CLI-linux%20·%20macOS%20·%20windows-blue)](https://github.com/softwarity/plug/releases)
[![CI](https://github.com/softwarity/plug/actions/workflows/ci.yml/badge.svg)](https://github.com/softwarity/plug/actions/workflows/ci.yml)

Run a local process as a member of your cluster: it resolves cluster service
names, reaches cluster services, and is itself reachable in the cluster under a
name — with no code change and no proxy settings in your app.

```bash
plug -s my-app:8080:3000 npm run start:dev
# reaches cluster services by name — and is itself reachable in the cluster
# as my-app:8080, forwarded to its local :3000
```

Prefix any command with `plug` and it joins the cluster by name — Node, the
JVM, Python, Go, curl, gRPC, database drivers, anything. Stop the command and
your machine is exactly as it was.

[![How plug works, animated: the cluster serves a request on its own, then the plug session parks the deployed service and your machine serves the same request](https://softwarity.github.io/plug/assets/about-diagram.svg)](https://softwarity.github.io/plug/)

## What you get

- Reach cluster services by their real names, from your laptop — no port-forwards
  to wire up, no `localhost:PORT` mappings, no `/etc/hosts` edits.
- Be reachable in the cluster under your own name — workloads call `my-app:8080`
  and land on your local process, for the life of the session.
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
    # required — the agent creates your -s name through it (see below)
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

The socket line is **required** on Docker, Compose and Swarm: it is how the
agent creates your `-s` name. It is root on the host, so mount it only on a
cluster you trust — the trust plug's no-auth transport already assumes.
Kubernetes needs no socket: the bundled manifest grants a Services-only RBAC
role instead — see [below](#the-name-in-the-cluster).

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
plug -s my-app:8080:3000 npm run start:dev
plug -s my-api:8080:8080 ./mvnw spring-boot:run
```

`-s name:cluster-port:local-port` is the name your process answers to in the
cluster; the same session reaches cluster services by name in return. It is
**required**: in a cluster a running process is a service, and a service has a
name — so name yours, even when nothing calls it back yet. The first run asks
which cluster to use and remembers it. Reaching another cluster is just naming it:

```bash
plug -p staging -s my-app:8080:3000 npm run start:dev   # asks once, then remembered
```

Everyday commands: `plug ls` (list clusters), `plug test` (check one is
reachable), `plug rn` / `plug rm` (rename / remove), `plug uninstall`,
`plug about`.

## Several clusters at once

Run the same process against two clusters in parallel — each stays isolated:

```bash
plug -p prod    -s my-app:8080:3000 npm run start
plug -p staging -s my-app:8080:3000 npm run start
```

Supported on all three OSes — proven simultaneously in CI on Linux, macOS and
Windows. See the [coverage matrix](https://softwarity.github.io/plug/#/coverage)
for the details.

## The name in the cluster

`-s name:cluster-port:local-port` publishes `name` in the cluster and forwards
`name:cluster-port` to your machine's `local-port`, for the lifetime of the
session — **no name pre-declared, no redeploy**. Any workload calling
`http://name:cluster-port` lands on your process. The agent creates the name on
the fly, which it does per engine:

- **Docker / Compose** — mount the Docker socket on the agent (required). Each
  `-s` spins up a tiny *signpost* container carrying the DNS alias, removed with
  the session.
- **Swarm** — same socket; the signpost is a Swarm *service*, which joins the
  stack's overlay whether or not it is `attachable`, so **no network change**.
  The agent just needs to run on a **manager** node (to create services).
- **Kubernetes** — no socket: the bundled [manifest](deploy/plug-k8s.yaml) grants
  a Services-only RBAC role, so `-s` creates and deletes the backing Service itself.
- **No socket** — the agent falls back to *static*: you pre-declare the name
  yourself (a network alias, a Service). It works, but you lose the on-the-fly
  provisioning — so mount the socket.

```yaml
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports: ["2222:22"]
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock   # required: the agent creates your -s name
```

The Docker socket is root on the host — mount it only on a cluster you trust
(the same trust plug's no-auth transport already assumes). plug verifies the
full path at startup so a missing name fails loud, and the port closes with the
session.

Developing a service that is **already deployed** in the stack? Its name is
taken — so plug **takes it over**: the deployed workload is parked for the
session (containers stopped, Swarm service scaled to 0, Kubernetes Service
repointed) and **restored when the session ends**, replica count included,
even across an agent restart. Your local process answers the name in its
place; afterwards the cluster is exactly as it was. A name held by another
live plug session is still refused.

## Let plug pick the local port

The cluster port is agreed in advance — it is what other workloads dial. The
**local** one is nobody's business but yours, and pinning it is what makes two
projects fight over `3000`, or the same app refuse to run on two branches at
once. Name it instead, and plug picks a free one per session:

```bash
plug -s web:8080:PORT  npm run dev -- --port={PORT}
```

`PORT` declares (bare — the third field of a `-s` can only ever be a port, so
there is nothing to disambiguate), `{PORT}` references it in the command
(braced — argv is free text, and a bare `PORT` would also rewrite
`--transport=PORTAL`). Any name works. The mapping is armed on that same
number, so the cluster reaches `web:8080` whichever port your process landed on
today.

The command line is the only channel — plug puts nothing in your process's
environment. One number, one way to hand it over, and no variable of yours
quietly overwritten.

Some things this makes easy:

```bash
# Two branches of the same service, side by side, both answering in the cluster
git worktree add ../hotfix && cd ../hotfix
plug -s api:8080:PORT ./mvnw spring-boot:run -Dserver.port={PORT}

# One process, two cluster names, one listener — the same name means one port
plug -s web:80:PORT -s web-tls:443:PORT node server.js --listen={PORT}

# A shared CI runner, where a pinned port is a race against the other jobs
plug -s e2e:8080:PORT npm run serve -- --port={PORT}
```

The two halves have to match, and plug says so at startup rather than let either
mistake through — both fail silently otherwise, from opposite ends:

- a `{TOKEN}` nothing declared reaches your command as the literal string
  `{PROT}`, which either crashes it or makes it fall back to a default port the
  cluster is not forwarding to;
- a name nothing references allocates a port your process is never told about —
  the cluster name gets published, and nothing ever answers it.

Commands that use braces for their own purposes (`awk '{print}'`) are untouched
when no `-s` names a port.

Pinning still works, and still makes sense when something outside the session
needs a stable address (a bookmarked URL, a debugger attach config).

Needs plug ≥ 2.4 on both sides: your launcher checks the mapping before it
connects, and the cluster's own core is what resolves it. `plug update` aligns
the two.

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

## License

plug is [FSL-1.1-Apache-2.0](LICENSE) — free for any purpose, including
building it into your own product, self-hosting it, or using it internally at
a company. The one thing it doesn't permit is a **competing use**: offering
plug itself, or a product/service that substitutes for it (e.g. a rival
hosted-gateway offering), to others. It converts to Apache-2.0 two years after
each release. Want to do exactly that anyway? A commercial license is
available — contact **[francois@hhdev.fr](mailto:francois@hhdev.fr)**.

Third-party components are listed in
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
