# Release Notes

## NEXT RELEASE — toward 1.0.0

- **`plug --tun` — total coverage on every runtime, one cross-platform data path**
  (`cli/internal/tun`). An opt-in root mode that captures the child's cluster
  traffic at the **IP layer** instead of the fd level: `wireguard-go/tun` opens a
  userspace TUN (`/dev/net/tun`, `utun`, WinTUN), a **gVisor** netstack terminates
  each TCP flow, and plug splices it to the SSH tunnel **by name** (a loopback DNS
  server mints a fake `240/4` IP per cluster name; the OS routes that range into
  the TUN). Because the child's socket is never touched, this covers **every
  runtime uniformly** — including the gRPC stacks (Netty, grpcio) the fd-level
  hook/supervisor can't. One Go codebase for Linux/macOS/Windows; needs root only
  to create the device + routes. The rootless hook/supervisor stays the default
  no-root path. **e2e: 28/28 green in TUN mode** (7 protocols × 4 languages).
- **Full process coverage — Go / statically-linked binaries are now covered on
  Linux** (`cli/internal/seccomp`). A rootless seccomp *user-notifier* traps the
  child's `connect(2)` at the kernel boundary and reroutes cluster connections
  through the same SOCKS proxy — so a Go binary, which bypasses libc for **both**
  name resolution (its pure-Go resolver) and the connection (a raw syscall),
  reaches cluster services **by name**, exactly like a libc app. No root, no
  daemon, no TUN: just an unprivileged seccomp filter + `process_vm_readv` on our
  own child. The supervisor degrades to a transparent `exec` wrapper wherever
  seccomp is denied, so wrapping is always safe. Opt out with `PLUG_NO_SECCOMP=1`.
  - **Coverage matrix.** Go on **macOS**: already covered (Go uses libSystem →
    the preload hook catches it). Go on **Linux**: covered (this supervisor).
    Native **Windows** (`ws2_32`): planned — needs a CI runner to build/test.
  - An **embedded DNS resolver** answers the child's own lookups so it never
    needs `/etc/resolv.conf` rewritten: single-label → cluster (fake IP, routed
    by name via SOCKS), dotted → resolved for real, `localhost` → loopback, AAAA
    → answered empty so it falls back to IPv4. The split-horizon and
    direct/external connectivity are preserved (loopback and real IPs are let
    through untouched).
- **E2E coverage matrix** (`e2e/` + `.github/workflows/e2e.yml`): a **languages ×
  protocols** grid — Go / Node / Python / Java clients, each with its natural
  driver, reaching **httpbin, postgres, redis, mongo, rabbitmq (AMQP), mosquitto
  (MQTT), gRPC** cluster services **by name** under plug. Run in **both modes**
  (one job per mode × protocol, isolated + parallel); the run Summary renders a
  grid per mode. Services track current majors (postgres 18, mongo 8, redis 8,
  rabbitmq 4…). **Rootless: 26/28** (the 2 gRPC gaps below). **TUN: 28/28.**
- **Fix — the app-level proxy env no longer fights the hook on Linux.** When the
  hook + supervisor are active they already intercept every `connect()`, so plug
  no longer also sets `HTTP_PROXY` / `-DsocksProxyHost` there: those made some JVM
  drivers hand the *fake* IP to the proxy (unroutable) and broke raw-TCP clients.
  Rerouting is now the hook/supervisor's job alone. (macOS unchanged.)
- **gRPC on Java and Python — the rootless-mode gap that motivated `--tun`.**
  In the rootless path these two still fail (`java:grpc`, `python:grpc`; the other
  26 cells pass); **`plug --tun` closes them** — IP-layer capture never touches
  the fd, so all 28 pass. Why the fd-level path can't: gRPC's HTTP/2 stacks arm
  their event loop (epoll) on the fd *before* `connect()` completes, and plug's
  only ways to hand over the tunnelled connection — `dup2()` in the hook, `ADDFD`
  in the supervisor — swap the fd's underlying file, stranding that registration.
  Go and Node register *after* connect, so they're fine. In-band fixes don't hold:
  - **Java (Netty):** negotiating on the app's *own* fd (no `dup2`) would keep the
    registration — but Java 21's `NioSocketImpl` makes **every** JVM socket
    non-blocking, so that path strands the whole JVM (JDBC, Jedis…), not just
    Netty. Reverted.
  - **Python (grpcio):** `GRPC_DNS_RESOLVER=native` fixes its c-ares DNS bypass,
    but grpcio then does a **raw** connect → the supervisor's `ADDFD` hits the
    same epoll-strand. Reverted.
  - A real fix likely needs a different hand-off (e.g. per-runtime epoll
    re-registration), tracked for later.
- Publishing is **CI-only**: the local Makefile was removed (the multi-arch
  image is built and pushed exclusively by CI; local builds are plain
  `go build` / `docker build`).
- Docs: the `forward =` mechanism clarified — it rewrites an env var for
  **Go/statically-linked** apps the transparent hook can't reach, not for libc
  drivers like `amqplib` (which the hook already handles by name).

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
