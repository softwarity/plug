# Release Notes

## NEXT RELEASE

### Fixed: macOS — the DNS watchdog no longer restarts the resolver on every configd event

The daemon re-asserts its system-DNS override whenever macOS recomposes the
network config — but it also flushed the cache and HUP'd `mDNSResponder` each
time. On a machine where something keeps configd busy (observed live: a
`locationd` Wi-Fi-scan loop re-publishing the DHCP lease ~2/min), that restarted
the system resolver all day and intermittently failed **unrelated** lookups
machine-wide (`Could not resolve host: github.com`…). The watchdog now rewrites
the overridden keys **quietly** when the effective config (Global/Setup and the
resolver files) still points at plug, and coalesces real flushes to at most one
per 30 s. `daemon.log` lines are now timestamped.

### New: takeover — develop a service that is already deployed

The name of a `-s` mapping often belongs to the very service you are developing,
already deployed in the stack — until now plug refused it and asked you to remove
the service by hand. Now plug **parks** the deployed workload for the session and
**restores it when the session ends**: `plug -s service1…` already states the
intent (the same behaviour as Telepresence's intercept and mirrord's steal).

- **Docker / Compose** — the containers answering the name are stopped, and
  restarted afterwards.
- **Swarm** — the service is scaled to 0, and scaled back **to its original
  replica count**. A stack's `<stack>_<svc>` service is recognized by its short
  alias; a foreign alias (a service whose own name is unrelated) is refused.
- **Kubernetes** — the existing Service is repointed at the agent (selector +
  ports), the originals saved **in an annotation on the Service itself**, and
  re-patched back afterwards. The bundled RBAC role gains `update`/`patch` on
  Services (still namespace-scoped, Services only).

The parking receipt rides the signpost's labels (or the k8s annotation), so the
restore survives anything: session end, `unserve-name`, **agent crash** (the
boot gc restores parked workloads before sweeping orphaned signposts), and a
transport reconnect **re-parks** (the same self-heal that re-provisions the
name). The signpost is created *before* the workload is parked, so the name
keeps resolving throughout — a no-record gap would leak lookups to the upstream
resolver (bench-proven on the embedded DNS).

A name held by **another live plug session** is still refused — takeover
applies to deployed workloads only, never to another dev's session. Against an
older agent (2.0.x) the CLI falls back to that agent's own behaviour (a taken
name is refused, with an upgrade hint). Switch-time caveats, one per platform:
on Swarm, callers holding a cached DNS answer (JVM ~30 s) may see brief
connection errors (the address behind the name changes). On Kubernetes the
Service keeps its **ClusterIP** through park and restore, so cached DNS stays
valid and *new* connections reach your session immediately — but the pods keep
running (only the name is rerouted): a parked k8s workload still consumes
queues, and a caller holding a pre-switch **keep-alive connection** keeps
reaching the old pod until it closes or idles out. Proven in CI on **all three
backends** (park → traffic lands locally → restore, on all three OSes) — on
Swarm including the scale-back to the *original* count (the CI target runs 2
replicas); agent-crash recovery bench-proven.

### CI: the whole e2e chain now runs against Kubernetes and Swarm too

Every push replays the full mesh e2e on **three cluster families**: the compose
cluster, a **kind** cluster (upstream Kubernetes), and a **single-node Swarm** —
same services, same names on each. On Kubernetes the agent is applied from the
**published** `deploy/plug-k8s.yaml` — RBAC included, only the image swapped —
so each push blesses the exact manifest users deploy: dynamic `-s` Services
through the Services-only role, the takeover repoint/restore, NodePort reach.
On Swarm the agent runs as a real **Swarm service on a non-attachable overlay**
(the prod shape): `-s` provisions its name as a Swarm-service signpost there,
and the takeover scales the deployed service to 0 and back. The 4×8 protocol
grid, multicluster, outage, gateway callback and collision run against every
family, natively from Linux, macOS and Windows. The image publishes only when
all nine legs are green.

---

## 2.0.0

### Breaking changes

- **`-s` is now mandatory when you run a command.** A running process in a cluster
  is a service, and a service has a name — so `plug` runs your command *as* a
  named member of the cluster:
  `plug [-p profile] -s <name>:<cluster-port>:<local-port> <command>`. Bare
  `plug <command>` now **errors**. **Migration:** add
  `-s <name>:<cluster-port>:<local-port>` to your invocations — name it even when
  nothing calls your process back (most of the time something will).
- **The Docker socket is required for `-s`** on Docker / Compose / Swarm (it is
  how the agent creates the name). Kubernetes uses a Services-only RBAC role
  instead. Without either, the agent falls back to a pre-declared *static* alias.
- **Needs an agent image from this release.** Against a pre-2.0.0 agent, plug now
  refuses with an upgrade hint instead of a cryptic error. With a *launcher*
  installed before this release, put `-s` after `-p`/`--host` (old launchers
  forward trailing flags they don't know straight to the core).

### New: serve a local service to the cluster (the reverse direction)

`plug -s <name>:<cluster-port>:<local-port> <cmd>` makes a local port reachable
from inside the cluster under a cluster DNS name, for the lifetime of the session
— every workload calling `<name>:<cluster-port>` lands on your machine, with **no
name pre-declared and no stack redeploy**. The agent provisions the name on the
fly: a *signpost* carrying the DNS alias — a **container** on Docker/Compose, a
**Swarm service** when the agent runs as a Swarm service (it joins the stack
overlay whether or not it's `attachable` — no network change; the agent runs on a
manager node), or a **Service** selecting the agent pod on Kubernetes
(Services-only RBAC) — created on `-s`, removed when the session ends, swept on
agent restart. The full path is verified at startup — a missing name, a too-old
agent image or a competing session **ends the session with the remedy**, never a
silent no-op — and the port closes with the session. After a transport reconnect
the mapping re-binds **and re-provisions** the name, so a restarted agent doesn't
leave it silently dead. Serving a name a real cluster service already owns is
**refused** (never a silent DNS round-robin on top of it), with the per-engine
remedy. The agent-side helper is the tunnel user's `ForceCommand` (`serve-name` /
`unserve-name` only, no shell).

e2e-proven on all three OSes with the **Docker backend** — every CI run serves a
name **declared nowhere**, then proves it two ways: a cluster workload fetches it,
AND an external caller POSTs to a **published cluster gateway** that calls the
name and round-trips a correlation id — and the full request path (root and a
deep path) — back to the runner's local service (the API-gateway use case, HTTP).
The **Swarm** and **Kubernetes** backends are coded and bench-proven but **not
yet driven by CI** (CI runs on Compose).

Security: the Docker socket is root on the host — enable dynamic provisioning
only on a trusted cluster; the Kubernetes grant is tight (Services only, namespace
scoped).

### Known limitations

- **Multi-node Swarm is unproven.** The Swarm-service backend is bench-tested on a
  single node only; `-s` relays to the agent's service VIP, which assumes the
  session's remote-forward lives on the one agent task. Run the agent as
  `replicas: 1` on a manager (global mode is refused for the same reason).
- **One agent per node.** The boot-time GC that sweeps a restarted agent's own
  orphaned signposts can, on a worker running *two* distinct plug agents, remove
  the other agent's live signpost. The shipped deploy pins the agent to a manager,
  so this needs a deliberate misconfiguration.
- **`-s` input mistakes fail loud, not silently:** two `-s` on the same
  cluster-port report "already exposed by another session?"; a duplicate name
  double-provisions. Both surface at startup.
- During an outage the transport's reconnect can briefly (≤ ~15 s, one dial
  timeout) stall other calls on that transport; already-open channels keep flowing.

---

## 1.4.0

- **Linux: the no-sudo privilege now survives cluster version changes.** The
  launcher promotes its file capabilities into the ambient set before exec'ing a
  downloaded core (they don't cross exec on their own), and the mount-ns shim
  clears them again before your command runs — no privilege leaks past plug.
  **One-time action**: launchers installed before this release lose the no-sudo
  privilege the first time their cluster changes version — re-run the cluster
  install once (`ssh get@<host> install | sh`).
- **macOS: cluster DNS is now self-healing and covers every resolver path.** A
  watchdog re-asserts plug's DNS override when macOS replaces it (DHCP renewal,
  network change — one event used to kill name resolution until `plug down`);
  manually configured DNS servers (Setup:) are overridden and restored too (they
  silently eclipsed plug for libresolv clients); and Go child binaries are routed
  to the pure-Go resolver (GODEBUG=netdns=go), killing a flat 5s-per-lookup mDNS
  detour on networks without a usable default resolver.
- **macOS: simultaneous multicluster is real (and now proven).** The global
  daemon already held one tunnel per active cluster with PID-at-connect
  attribution; docs claimed "one cluster at a time" — the CI multicluster cell
  now proves two live clusters simultaneously on macOS (same name, right
  backend), like Linux and Windows.
- **Linux: simultaneous clusters fixed.** Each launch now claims its own TUN
  device slot (plug0, plug1, …) with its own fake-IP subnet — a second
  simultaneous cluster used to die on "device or resource busy".
- **CI now tests the real user flow, end to end.** Every run installs plug FROM
  the cluster on all three OSes (the exact one-liners, real privilege grants),
  runs the 4-language × 8-protocol grid natively over a mesh, asserts
  multicluster on all three, and checks that the last PUBLISHED launcher still
  drives this branch's core. Images publish only when everything is green.

---

## 1.3.0

- **Windows: no-admin data path, validated end-to-end on a real machine.** The Windows
  data path (WinTUN + routes + DNS) now lives in a **SYSTEM service** — the SCM
  counterpart of the macOS daemon. Install it once from an **elevated Git Bash**; after
  that every `plug <cmd>` is a **non-elevated** launcher that starts the service via its
  ACL (Authenticated Users may start it) and delegates to it — proven from a genuinely
  non-elevated (LIMITED-token) process. Several clusters run **side by side**: the service
  holds one tunnel per cluster and attributes each flow by process ancestry at connect
  (validated on two live clusters + concurrent same-cluster sessions).
- **Windows: real cluster access by name.** `plug curl http://my-service:8081/…` resolves
  the single-label cluster name and reaches the service. Windows never queries a *bare*
  single-label name (LLMNR/NetBIOS only), so plug advertises a **search suffix** on the
  WinTUN adapter (`my-service` → `my-service.plug`), routes `.plug` to the in-stack
  resolver via an **NRPT** rule, and strips the suffix back — the Tailscale/WireGuard
  mechanism. (The launcher also handles the `.exe` suffix and `wintun.dll` beside a
  downloaded version.)
- **Windows installer is pure Git Bash.** `ssh get@<host> install-windows | bash -s -- <host> <port>`
  — bash, not PowerShell: a piped bash script's `exit` is reliable (a piped `powershell -Command -`
  was not, so a failed install used to run on with misleading output). The host is passed as an
  argument (`bash -s -- <host> [port]`), since the MSYS ssh streaming the script can't have its
  command line read. It fetches plug.exe **and wintun.dll from the agent** (no wintun.net
  dependency, no more intermittent fetch), sets PATH + a profile, and installs the service when
  elevated (else it tells you to re-run elevated).
- **One way to point at a cluster.** The cluster comes from `--host`/`--port` or a profile; the
  `$PLUG_HOST`/`$PLUG_PORT` environment fallback was removed (it duplicated the flags and muddied
  the precedence).
- **Windows cold-start ~15 s → ~0.8 s.** The NRPT rule goes in via the **registry** instead
  of two PowerShell starts (~3 s), the reconcile opens a cluster tunnel in ~0.3 s, and an
  idle tunnel is held for a short grace so back-to-back runs reuse it (~0.3 s). No DNS
  hijack is left in place while idle — short local names resolve normally between runs.
- **Concurrent sessions no longer knock each other out.** A channel the agent *rejects*
  (a bare name that isn't a cluster service — Windows probes plenty, e.g. WPAD) no longer
  triggers a reconnect that tore down every other channel on the shared SSH connection;
  only 1 of N concurrent `plug`s used to survive. Cross-platform (shared transport).
- **Version probe & download use crypto/ssh on Windows** (like the data tunnel) instead of
  the external `ssh` binary, which hangs on Windows when its stdout is captured over a pipe
  — that had frozen every `plug` at the version probe.
- **Attribution hardened against PID recycling.** The by-process router now stamps
  each hop of the ancestry walk with the process's start time and refuses a
  temporally impossible chain — an "ancestor" that started *after* its child is a
  recycled PID (same number, new unrelated process), so the walk aborts rather than
  misroute the flow to that stranger's cluster. This matters most on Windows, which
  (unlike unix) never re-parents an orphan, so a dead parent's PID lingers in the
  child; the guard is wired and unit-tested on all three OSes, and preps the Windows
  daemon.

---

## 1.2.0

- **Simultaneous clusters on macOS.** A single global datapath daemon now holds
  one tunnel per cluster and routes each connection to the right one by the
  calling process — so `plug -p a <cmd>` and `plug -p b <cmd>` run at the same
  time, each reaching its own cluster (Linux already did this via mount
  namespaces). Windows attribution bricks are in; its daemon is next.
- **Profiles: create by naming, no separate `init`.** Reaching a new cluster is
  just `plug -p <name> <command>` (wizard on first run, then remembered).
  `plug -p <name> -H <host> [--port <p>]` defines it non-interactively, with or
  without a command to run; `plug test -H <host>` probes an agent without saving
  anything. `plug init` is gone from the help (still works).
- **Leaner CLI help.** `plug -h` lists the everyday commands only — no
  implementation talk. The concept moved to `plug about`. `self-update` and (on
  macOS) `down` are unlisted: versions auto-update on connect, and the datapath
  tears itself down after the last `plug` exits.
- **macOS: `plug <command>` now runs without sudo (setuid-root helper).** The
  install posts the launcher as a setuid-root helper (`chown root:wheel` +
  `chmod u+s`, one sudo at install — the macOS counterpart of the Linux `setcap`),
  so day-to-day `plug <command>` needs no sudo, matching Linux. plug starts with
  euid 0 to hold the utun + DNS, then **drops your command back to your own user**
  before running it — unlike a Linux capability (dropped for free across exec), a
  setuid euid is inherited, so the child is spawned under your uid/gid and
  supplementary groups. `sudo plug` still works (it drops via `SUDO_UID`); a
  genuine root login runs the child as root, unchanged. `self-update` re-applies
  the setuid bit so an update doesn't silently disable the helper. Off localhost,
  the pinned `known_hosts` (written by the euid-0 daemon) is chowned back to you,
  so you can act on a "key changed" warning without sudo.
- **Native Windows installer.** `ssh get@host install-windows | powershell
  -NoProfile -Command -` mirrors the unix `install | sh`: it downloads `plug.exe`
  + `wintun.dll` into `%LOCALAPPDATA%\Programs\plug`, adds it to PATH, and
  pre-creates your profile — no admin needed to install (no WSL2). Launch still
  needs an elevated terminal for now (WinTUN); a persistent SYSTEM service is the
  planned "run without admin" path.
- **Kubernetes manifest modernized.** `deploy/plug-k8s.yaml` now describes the
  actual TUN data path (not the removed SOCKS proxy), with a TCP readiness/liveness
  probe and modest resource limits. `kubectl exec` transport
  was evaluated and dropped — `kubectl port-forward` already gives a zero-exposed
  port gated by API-server RBAC.
- **Multicluster (macOS/Windows) — design + attribution core.** The validated
  approach routes by PID **at connect** (not at DNS): one system resolver, fake IPs
  per name, and the flow attributed to a cluster by walking the connecting
  process's ancestry to its `plug -p X` launcher. The attribution core landed
  (isolated, unit-tested, wired into no live datapath).
  Linux multicluster already works via mount namespaces.
- **e2e coverage: WebSocket** across all four language clients (Go/Node/Python/Java).

---

## 1.1.0

- **macOS DNS fix — real apps resolve cluster names again.** A real app's
  `getaddrinfo(<service>)` used to return `ENOTFOUND` on macOS: the datapath was
  fine, but macOS resolves through mDNSResponder/SystemConfiguration, not
  `/etc/resolv.conf`. DNS is now served **at the IP layer** — a gVisor UDP
  forwarder answers `:53` on a dedicated fake IP (`198.18.<N>.53`) reached through
  the TUN — and the **system** resolver is repointed at it through each OS's
  native channel: the SystemConfiguration **dynamic store** (`scutil`) on macOS
  (`networksetup` can't touch a VPN's primary service, so it failed silently), a
  per-child private `resolv.conf` on Linux, the adapter DNS (winipcfg) on Windows.
  Proven end-to-end on macOS with an active corporate VPN. No `LD_PRELOAD`/DYLD
  interposition — coverage stays universal (Go static and gRPC included).
- **Per-instance partition.** The fake range is carved into per-instance `/24`s
  (`198.18.<N>.0/24`, DNS at `.53`, never minted), laying the groundwork for
  multicluster.
- **macOS: a persistent per-cluster daemon holds the datapath across restarts.**
  Because macOS repoints DNS machine-wide, the datapath can't die with each
  `plug <cmd>`. It now lives in a small daemon, started on demand and detached:
  `plug <cmd>` just ensures it's up, registers as a client, and runs the child (no
  tunnel of its own). Restart your processes freely — resolution survives. The
  daemon tears down and restores your DNS 30s after the last `plug` of the cluster
  exits; `plug down` stops it now; a hard kill is repaired from a DNS backup on the
  next `plug`. Linux is unchanged (autonomous per launch via mount namespaces).
- **Known macOS limits.** One active cluster at a time (the system resolver is
  global). Simultaneous *different* clusters on macOS/Windows is planned
  (transparent PID-routed, or suffix-based).

---

## 1.0.0

- **One mode: the userspace TUN, over the SSH tunnel** (`cli/internal/tun`). plug
  captures the child's cluster traffic at the **IP layer**: `wireguard-go/tun`
  opens a userspace TUN (`/dev/net/tun`, `utun`, WinTUN), a **gVisor** netstack
  terminates each TCP flow, and plug splices it to the agent **by name** (a
  loopback DNS server mints a fake `240/4` IP per cluster name; the OS routes that
  range into the device). The child's socket is never touched, so it covers
  **every runtime uniformly** — libc, Go/statically-linked, and the gRPC HTTP/2
  stacks (Netty, grpcio) that fd-level interception strands. One Go codebase for
  Linux/macOS/Windows; it needs root to create the device + routes, set up once by
  the cluster install (a root helper), so day-to-day it's just `plug <command>`.
- **The rootless fd-level machinery is gone.** The LD_PRELOAD hook, the seccomp
  supervisor, the SOCKS5/HTTP proxies and the env-proxy wiring existed only to work
  *without* root; the TUN covers everything they did and more, with far less code
  and no per-runtime gaps. There is no mode flag — the TUN **is** the mode.
- **E2E coverage matrix** (`e2e/` + `.github/workflows/e2e.yml`): a **languages ×
  protocols** grid — Go / Node / Python / Java clients, each with its natural
  driver, reaching **httpbin, postgres, redis, mongo, rabbitmq (AMQP), mosquitto
  (MQTT), gRPC** cluster services **by name** under plug. One CI job per protocol;
  the run Summary renders the full grid. Services track current majors (postgres
  18, mongo 8, redis 8, rabbitmq 4…). **28/28 green** — gRPC on the JVM and CPython
  included, the exact cases the old fd-level path could never pass.
- **`plug selftest` + native macOS/Windows/Linux CI.** A self-contained smoke that
  loops real traffic through a real TUN device **by name** with no agent and no
  Docker. CI builds plug natively on each OS, runs the unit suite, then runs the
  selftest under sudo (macOS/Linux) / WinTUN (Windows) — the visible proof that the
  data path works on each platform, not just that it compiles.
- Publishing is **CI-only**: the multi-arch image is built and pushed exclusively
  by CI; local builds are plain `go build` / `docker build`.

---

## 0.2.0

- Rootless `plug uninstall` — no sudo unless the retired root daemon left files.
- Install one-liner skips the host-key check (the agent regenerates its key at
  each start; it is not a secret) — matching what plug does internally, so a
  redeployed agent no longer breaks reinstalls.
- Local `make` builds carry the git rev (`dev+<rev>`), like CI.

---

## 0.1.0

### Data path

- Rootless, per-process tunnel to a tiny agent container over SSH
  `direct-tcpip`, so cluster DNS names resolve and services are reachable — no
  root, no TUN, no daemon; several clusters run side by side.
- Transparent `connect()` / `getaddrinfo()` / `gethostbyname()` injection (the
  "N1" hook, `DYLD_INSERT_LIBRARIES` / `LD_PRELOAD`) so any **libc** runtime
  (Node, the JVM, Python, curl…) reaches raw-TCP services — `amqplib`, `pg`,
  `mongodb`, `redis`, gRPC — with no per-service config.
- **Split-horizon routing** by name shape: single-label names → cluster, dotted
  FQDNs and `localhost` → direct, with mutual fallback. `PLUG_DIRECT` forces
  extra CIDRs / hosts / suffixes direct.
- HTTP proxy (`HTTP_PROXY` / `HTTPS_PROXY`) + SOCKS5 proxy (`ALL_PROXY`,
  `JAVA_TOOL_OPTIONS=-DsocksProxyHost`) for proxy-aware clients and the whole JVM.
- Per-session port-forwards for what the hook can't reach (Go/static, non-TCP).

### Reliability & security

- **Self-healing transport**: SSH keepalive + transparent reconnect + bounded
  channel opens — an idle NAT / VPN / LB drop no longer requires restarting plug.
- Handshake timeouts on tunnelled connections (no more hangs), preserved socket
  options (`TCP_NODELAY`, `SO_KEEPALIVE`), and a dynamic fake-IP table (no cap).
- **Host-key pinning (TOFU)** in `~/.plug/known_hosts` — a MITM tripwire on top
  of the deliberately no-secret transport.

### Install & versioning

- Install from the cluster: `ssh get@<host> install | sh` — binaries embedded in
  the installer, picked by `uname`; a per-host profile is pre-created from your
  own `ssh` command.
- Launcher model (like `nvm` / `rustup`): each cluster runs its exact matching
  version, cached under `~/.plug/versions/`. Version carries a build id
  (`<version>+<git-rev>`) so `latest` rebuilds are detected.
- Profiles in `~/.plug/*.conf`, auto-selected, with `ls` / `rm` / `rn` / `test`;
  `--host` / `--port` / `$PLUG_HOST` / `$PLUG_PORT` bypass.

### Packaging

- Docker image renamed `softwarity/plug-agent` → **`softwarity/plug`** (the
  Swarm/k8s service is named `plug`).
- Native multi-arch agent image (linux/amd64, linux/arm64); CLI for Linux and
  macOS (Windows via WSL2).

### Known limits

- **libc + TCP only.** Go / statically-linked binaries and non-TCP (UDP/QUIC)
  use a port-forward. IPv6 is treated as IPv4 (v6-only apps / v6 literals are not
  tunnelled). macOS SIP system binaries and hardened apps bypass injection (the
  env-proxy still applies). No authentication by design — trusted dev clusters only.

---
