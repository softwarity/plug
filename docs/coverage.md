# Coverage matrix — features × OS

Legend: ✅ works · ⚠️ partial / unproven · ❌ not yet · — n/a by design

_Snapshot 2026-07-07 (branch `main`). "proven" = validated at runtime; "coded" = compiles + unit-tested but not e2e-validated._

## Install & privilege

| Feature | Linux | macOS | Windows | Notes |
|---|:--:|:--:|:--:|---|
| Install from the cluster (one-liner) | ✅ | ✅ | ✅ | `install \| sh` (unix) · `install-windows \| powershell` |
| No sudo/admin **to install** | ✅ | ✅ | ✅ | Windows installer needs no admin |
| One-time privilege grant at install | ✅ | ✅ | ❌ | setcap (Linux) · setuid helper (macOS) · none on Windows |
| `plug <cmd>` **without sudo/admin** | ✅ | ✅ | ❌ | Windows: elevated terminal per session |
| Child runs as **you** (privilege drop) | ✅ | ✅ | — | caps don't cross exec (Linux) · `Credential` drop (macOS) · process stays elevated (Windows) |
| `self-update` preserves the privilege | ⚠️ | ✅ | — | setcap lost on update (pre-existing) · macOS re-applies setuid |
| Pre-create profile at install | ✅ | ✅ | ✅ | reads host/port off the live `ssh` |
| Uninstall (`plug uninstall`) | ✅ | ✅ | ⚠️ | unix covered; Windows path unverified |

## Data path

| Feature | Linux | macOS | Windows | Notes |
|---|:--:|:--:|:--:|---|
| Userspace TUN (IP-layer capture) | ✅ | ✅ | ✅ | `/dev/net/tun` · `utun` · WinTUN (winipcfg/LUID) |
| Cluster-name DNS (real apps) | ✅ | ✅ | ✅ | private resolv.conf (mnt-ns) · scutil store · adapter DNS |
| Works under a corporate VPN | ✅ | ✅ | ⚠️ | macOS proven with GlobalProtect; Windows unproven |
| Every runtime (Node/JVM/Py/Go/gRPC) | ✅ | ✅ | ✅ | IP-level capture, socket never touched |
| Native `selftest` (datapath proof) | ✅ | ✅ | ✅ | green on all three in CI |
| e2e protocol matrix (8 protos ×4 langs) | ✅ | — | — | Docker-based → Linux; mac/win proven via native selftest |
| Split-horizon (short→cluster, FQDN→direct) | ✅ | ✅ | ✅ | + `PLUG_DIRECT` overrides |
| Self-heal (VPN/ sleep / agent restart) | ✅ | ✅ | ⚠️ | keepalive+reconnect; Windows unproven |

## Daemon / persistence

| Feature | Linux | macOS | Windows | Notes |
|---|:--:|:--:|:--:|---|
| Persistent per-cluster daemon | — | ✅ | ❌ | Linux autonomous (no need) · Windows service = WIP |
| Survives process restarts | ✅ | ✅ | ⚠️ | Linux per-launch · macOS daemon · Windows per-launch |
| Graft (multi-process, same cluster) | ✅ | ✅ | ✅ | flock leader/graft (macOS) · autonomous elsewhere |
| `plug down` | — | ✅ | — | only meaningful with the macOS daemon |

## Multicluster (different clusters at once)

| Feature | Linux | macOS | Windows | Notes |
|---|:--:|:--:|:--:|---|
| Simultaneous different clusters | ✅ | ✅ | ⚠️ | Linux mount-ns; **macOS validated e2e** (PID-at-connect); Windows: bricks ready, daemon TODO |
| PID-at-connect attribution wired | — | ✅ | ❌ | macOS global daemon routes each flow by PID — proven on two live clusters |
| ↳ `ppidOf` | ✅ | ✅ | ⚠️ | /proc · `ps` · ToolHelp (coded, not yet run on Windows) |
| ↳ `pidForLocalPort` | ✅ | ✅ | ⚠️ | /proc/net/tcp+fd · lsof · GetExtendedTcpTable (coded, not yet run) |
| N-tunnel global daemon | — | ✅ | ❌ | macOS done + validated; Windows = SYSTEM service TODO |

## Profiles & CLI

| Feature | Linux | macOS | Windows | Notes |
|---|:--:|:--:|:--:|---|
| Profiles `~/.plug/*.conf` | ✅ | ✅ | ✅ | `init` `ls` `rm` `rn`/`mv` `test` |
| `-p <profile>` / `--host` / `--port` / env | ✅ | ✅ | ✅ | env = `$PLUG_HOST` `$PLUG_PORT` |
| Launcher versions (per-cluster) | ✅ | ✅ | ✅ | `versions` · `self-update` · cache chowned to user (macOS) |
| Host-key TOFU pin | ✅ | ✅ | ✅ | localhost skipped; pin chowned to user (macOS) |
| Port-forward escape hatch (`forward=`) | ✅ | ✅ | ✅ | rewrites an env var to a local port |

## Agent deployment

| Feature | Support | Notes |
|---|:--:|---|
| Docker Compose / Swarm | ✅ | agent image joins the stack network |
| Kubernetes (NodePort) | ✅ | `deploy/plug-k8s.yaml`, `--port 32222` |
| Kubernetes (`kubectl port-forward`) | ✅ | zero exposed port, API-server RBAC |
| Cross-namespace | ✅ | via FQDN `svc.othernamespace` |
| `kubectl exec` transport | ❌ | dropped — port-forward already covers it |

## Design limits (all OS)

| Limit | Status | Notes |
|---|:--:|---|
| UDP / QUIC / ping | ❌ | TCP only (SSH tunnel); most clients fall back to TCP |
| IPv6 literal (hard-coded) | ❌ | fake IPs are IPv4; by-name cluster service is fine |
| Root/helper required | — | by design — the price of covering every runtime |
| Authentication | — | none by design — trusted dev clusters only |

## Biggest holes (priority order)

1. **Windows "no-admin" run** — needs a persistent SYSTEM service (like the macOS daemon) driven over IPC. Installer + datapath done; elevation model missing.
2. **Multicluster on Windows** — macOS is done and validated end to end on two live clusters; Windows has the attribution bricks (coded) but still needs the SYSTEM-service N-tunnel daemon.
3. **Windows real-app + VPN** — datapath (selftest) green, but real end-to-end use and VPN behaviour unproven.
4. **`self-update` on Linux** loses file-caps (pre-existing; macOS already re-applies its bit).
