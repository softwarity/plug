# plug

[![release](https://img.shields.io/github/v/release/softwarity/plug?label=release)](https://github.com/softwarity/plug/releases)
[![docker](https://img.shields.io/docker/v/softwarity/plug?sort=semver&label=docker)](https://hub.docker.com/r/softwarity/plug)
[![docker pulls](https://img.shields.io/docker/pulls/softwarity/plug)](https://hub.docker.com/r/softwarity/plug)
[![license](https://img.shields.io/github/license/softwarity/plug)](LICENSE)
[![arch](https://img.shields.io/badge/arch-amd64%20·%20arm64-brightgreen)](https://hub.docker.com/r/softwarity/plug/tags)
[![CLI](https://img.shields.io/badge/CLI-linux%20·%20macOS%20·%20windows-blue)](https://github.com/softwarity/plug/releases)
[![CI](https://github.com/softwarity/plug/actions/workflows/ci.yml/badge.svg)](https://github.com/softwarity/plug/actions/workflows/ci.yml)

Run a local process **as if it were inside your cluster** — cluster DNS names
resolve, cluster services are reachable, with **no code change and no proxy
config** in your app.

```bash
plug npm run start:dev
# your local NestJS/Spring/Quarkus now resolves and reaches
# http://my-service:8080 like any workload in the cluster
```

## How it works

**One mechanism: a userspace TUN, over an SSH tunnel.** plug captures the child's
cluster traffic at the **IP layer** and splices it, by name, to a tiny agent
running in the cluster.

```
┌─ your machine ─────────────────┐        ┌─ cluster ───────────────────┐
│  plug <cmd>   (root / helper)  │        │  plug agent (alpine + sshd) │
│   └ userspace TUN (gVisor)     │        │                             │
│      ├ DNS answered in-stack   │──ssh───┼─→ direct-tcpip: sshd        │
│      │  on a fake IP:53        │  :2222 │   resolves the name and     │
│      └ splices each flow ──────┼────────┼─→ dials service:port from   │
│  <cmd> runs unchanged;         │        │   inside the cluster        │
│  its socket is never touched   │        │                             │
└────────────────────────────────┘        └─────────────────────────────┘
```

- A `wireguard-go` device (`/dev/net/tun`, `utun`, WinTUN) feeds a **gVisor
  userspace netstack**. DNS is answered **in-stack** on a dedicated fake IP
  (`198.18.<N>.53:53`), minting a fake IP per single-label cluster name; the OS
  routes that range into the TUN, so the child's `connect()` surfaces as a packet
  plug reads, terminates, and splices to the SSH tunnel **by name**.
- Because capture is at the IP layer, **the child's socket is never touched** — so
  it covers **every runtime with no config**: Node, JVM (Spring/Quarkus/Netty),
  Python, Ruby, PHP, curl, **Go and other statically-linked binaries, and gRPC**
  included (the cases an `LD_PRELOAD`/proxy approach cannot do).
- **Split-horizon by name shape**: single-label names (`my-service`, `rabbitmq`)
  go to the cluster; dotted FQDNs (`api.github.com`) and `localhost` resolve and
  connect **directly**, so your app keeps normal internet access.
- **Self-healing**: the tunnel survives a VPN reconnect, a laptop sleep, or an
  agent restart.

It needs **root** (create the TUN + set routes + repoint DNS) — granted **once at
install** so day-to-day `plug <cmd>` runs with no sudo (see below).

## Install

**On the cluster** — add the agent to your stack; it joins the stack network:

```yaml
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports: ["2222:22"]
```

Standalone agent for several stacks: [deploy/plug-stack.yml](deploy/plug-stack.yml).
Kubernetes: see [Kubernetes](#kubernetes) below.

**On each dev machine** — install straight from the cluster, one line. The agent
serves the right binary; the installer reads the cluster address from *your* `ssh`
command and saves a profile named after that host, so plug is ready immediately.

```sh
# Linux / macOS — the agent regenerates its host key at each start (not a secret
# in plug's model), so skip the host-key check:
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@<cluster-host> install | sh
```

The install grants plug its privilege **once** so future runs need no sudo — this
is the OS-native equivalent of the same thing:

| OS | Privilege granted at install | Day-to-day |
|---|---|---|
| **Linux** | `setcap cap_net_admin,cap_sys_admin,cap_net_bind_service` (one sudo) | `plug <cmd>` — no sudo |
| **macOS** | setuid-root helper (`chown root:wheel` + `chmod u+s`, one sudo); plug starts euid 0 to hold the TUN + DNS, then **drops your command back to your user** | `plug <cmd>` — no sudo |
| **Windows** | see [Windows](#windows) | *Administrator per session (helper WIP)* |

On macOS the data path lives in a small **per-cluster daemon** (started on demand,
detached): because macOS repoints DNS machine-wide, the datapath must survive each
`plug <cmd>`. Restart your processes freely — resolution survives; the daemon
tears down and restores your DNS 30 s after the last `plug` of the cluster exits,
and `plug down` stops it now.

Build from source instead: `go build -o plug ./cli`.

### Versions — the launcher model

plug is a small **launcher** (like `nvm`/`rustup`). On each run it asks the agent
which version it speaks and executes *that exact version* from `~/.plug/versions/`,
downloading it once if missing. Each cluster runs its own matching version.
`plug versions` lists what's cached; `plug self-update` refreshes the launcher.

## Usage

```bash
plug npm run start:dev
plug ./mvnw spring-boot:run
plug curl http://my-service:8080/health
```

Profiles live in `~/.plug/*.conf` and are picked automatically: no profile → a
short wizard; one profile → used as is; several → interactive, or `-p staging`.
Reaching a new cluster is just naming it — the profile is created on first use:

```bash
plug -p staging <command>                 # wizard on first run, then remembered
plug -p staging -H node --port 2222        # define it non-interactively (no wizard)
plug -p staging -H node <command>          # define it and run, in one line
plug test -H node                          # probe an agent without saving anything
```

`--host`/`--port` (or `$PLUG_HOST`/`$PLUG_PORT`) target an agent directly.
`PLUG_DIRECT=<cidr,host,suffix,…>` forces extra destinations to bypass the cluster.

CLI: `plug ls` · `plug test` · `plug rn`/`rm` · `plug versions` · `plug uninstall`
· `plug about`. (`plug self-update` and — on macOS — `plug down` still work; they're
just rarely needed: versions auto-update on connect, the datapath tears itself down.)

## Multiple clusters at once

- **Linux**: already supported — each launch gets a private resolver in its own
  mount namespace, so `plug -p a <cmd>` and `plug -p b <cmd>` run side by side.
- **macOS / Windows**: **one active cluster at a time** today (the system resolver
  is machine-wide). Simultaneous *different* clusters is designed (transparent,
  bare names, disambiguated at `connect()` by process ancestry) — see
  [docs/multicluster.md](docs/multicluster.md).

## Kubernetes

Deploy the agent in the **namespace of the services you want to reach**:

```
kubectl -n <your-namespace> apply -f deploy/plug-k8s.yaml
```

`deploy/plug-k8s.yaml` is a Deployment + NodePort Service (`32222` → container
`22`). sshd resolves short service names (`myservice`) via the pod's resolver
(CoreDNS) from inside that namespace and dials them itself over an SSH
`direct-tcpip` channel — no subnet or CIDR to declare. Reach it two ways:

- **NodePort**, on any node: `plug --host <a-node> --port 32222 <cmd>`
- **`kubectl port-forward`** — nothing exposed on the cluster; the tunnel rides
  the API server and is gated by its RBAC:

  ```
  kubectl -n <ns> port-forward svc/plug 2222:2222
  plug --host localhost <cmd>
  ```

Short names only resolve within the agent's namespace — for a service elsewhere,
use its FQDN (`myservice.othernamespace`). See [deploy/README.md](deploy/README.md).

## Windows

Native Windows is supported (no WSL2 needed). Install straight from the cluster,
one line — same model as unix, just piped into PowerShell instead of `sh`:

```powershell
# host key regenerated each start (not a secret) — skip the check, as plug does internally:
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=NUL get@<cluster-host> install-windows | powershell -NoProfile -Command -
```

This downloads `plug.exe` and `wintun.dll` into `%LOCALAPPDATA%\Programs\plug`,
adds it to your PATH, and pre-creates a `~/.plug/<host>.conf` profile from your
`ssh` line. Installing needs **no administrator rights**. Requires the built-in
Windows OpenSSH client (*Settings → Apps → Optional Features → OpenSSH Client*).

**Running — the one caveat.** plug's Windows data path is a WinTUN adapter, and
creating it plus its routes requires **Administrator**. Unlike Linux (`setcap`) or
macOS (setuid helper) — where one grant at install lets every later run start
unprivileged — Windows has no per-binary privilege bit, and the process holding
the adapter is the same one running your command in the foreground. So **for now,
start `plug` from an elevated terminal** (*Run as administrator*):

```powershell
plug npm run start:dev
```

A future release will move the Windows data path into a persistent **SYSTEM
service** (like the macOS daemon) driven by a non-elevated launcher over IPC —
the path to "install once, run without admin" while keeping your command attached
to your terminal. A scheduled task alone can't (it runs detached from the console).

## Security model — read this

**There is deliberately no authentication.** The SSH keypair is embedded in this
repository and in every `plug` binary; it is a transport detail, not a secret.
Anyone who can reach the agent port has full network access to the attached
cluster networks. Only deploy the agent on clusters you already trust; never
publish port 2222 on an untrusted network. The agent's host key is pinned on first
use (`~/.plug/known_hosts`) — a changed key aborts the connection (a basic MITM
tripwire on top of the no-secret transport).

## Limits (by design)

- **TCP only** — the SSH tunnel carries TCP, so UDP/QUIC/ping aren't tunnelled
  (most clients fall back to TCP; HTTP/3 forced to QUIC would not).
- **IPv6 literals** — fake IPs are IPv4; an app that connects to a hard-coded IPv6
  isn't tunnelled (a cluster service reached **by name** is fine).
- **Root/helper required** — the price of covering every runtime uniformly.

## Roadmap

- [x] Userspace-TUN data path (covers every runtime incl. Go & gRPC), split-horizon
      routing, self-healing transport, host-key TOFU
- [x] Install from the cluster + per-cluster launcher versions + one-sudo privilege
      (setcap on Linux, setuid helper on macOS)
- [x] macOS DNS at the IP layer (works under a corporate VPN) + persistent
      per-cluster daemon
- [x] Kubernetes manifest — NodePort or `kubectl port-forward`
- [ ] Multicluster on macOS/Windows (PID-at-connect) — [design](docs/multicluster.md)
- [ ] Windows "no-prompt admin" helper (elevated task/service) — see [Windows](#windows)
- [ ] IPv6 fake-pool + v6-literal tunnelling
- [ ] Generalize the multi-protocol selftest per OS

📊 [Coverage matrix](docs/coverage.md) — what works on which OS (Linux · macOS · Windows), feature by feature.

Distribution is **from the cluster only** (one source: the agent image) — no
Homebrew tap or separate package channel by design.
