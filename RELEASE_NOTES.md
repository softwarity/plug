# Release Notes

## NEXT RELEASE

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
