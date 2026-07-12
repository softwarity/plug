# Multicluster — design (PID-at-connect)

Status: **shipped and validated on Linux and Windows.** Linux uses per-launch mount namespaces and needs no attribution; Windows uses the SYSTEM service with the attribution core (`cli/internal/tun/pidroute*.go`) routing each flow by PID at connect, validated on two real clusters. **macOS** shares the same attribution code, but its daemon still holds one cluster at a time — generalizing it to N tunnels and validating it is the remaining work (see the bottom of this page).

## The problem

Running two *different* clusters at once on the same machine.

- **Linux is already there.** Each `plug` launch gets a private `/etc/resolv.conf` bind-mounted in its own mount namespace, so two launches never share DNS — `plug -p A` and `plug -p B` are isolated for free.
- **macOS / Windows are the hard case.** There is no per-process resolver: plug repoints the **system** resolver (macOS `scutil` dynamic store, Windows adapter DNS), which is **machine-wide**. Two clusters would fight over one resolver. The PID-at-connect approach below now runs several side by side on **Windows** (SYSTEM service); on **macOS** the same design applies, but the daemon still holds one cluster at a time for now.

## Why not suffixes

The obvious idea — plug appends a per-cluster suffix so `postgres` becomes `postgres.clusterB` — was examined and **rejected on macOS**:

- To rewrite what the *app* resolves you must sit **inside** the app's `getaddrinfo`. That is the `LD_PRELOAD`/`DYLD` hook the new TUN architecture deliberately removed.
- A **search domain** can't do it either: it is global on macOS (not per-process), and the bare name `postgres` succeeds first (default cluster), so the suffix fallback never fires. It also can't tell app A from app B.

Suffixes remain viable **on Linux** (the private `resolv.conf` can carry `search clusterB`), but they are not a cross-platform answer.

## The validated approach — route by PID, at connect

Decouple *naming* from *routing*:

1. **One system resolver, one shared fake space.** The daemon repoints DNS once and mints a fake IP **per name**, shared across clusters (`postgres` → one fake IP whichever cluster owns it). No per-cluster DNS.
2. **Disambiguate at `connect()`, not at DNS.** When the app connects the fake IP, the packet reaches plug's netstack (`handleTCP`). *There* plug has both the **name** (via the fake table) and, crucially, a handle on the **caller**: the flow's source port.
3. **Attribute the flow to a cluster by ancestry.** Source port → owning PID (OS TCP table) → walk the parent chain → the first ancestor that is a registered `plug -p X` launcher → its cluster. Route the `(name, cluster)` pair into that cluster's tunnel.
4. **`plug -p X <cmd>` is the entry point** (unchanged — you already type it). It doesn't change *where* DNS goes; it **marks the process ancestry** so the daemon can attribute connects to cluster X.

Transparent (bare names everywhere), at the cost of one PID lookup per connection (cached `PID→cluster`).

### The one honest limit: "refuse en cas de doute"

A process **detached** from its plug launcher (`setsid`, reparented to `launchd`/`init`) can't be walked back to a cluster. Rather than guess and route to the wrong cluster, plug **refuses** the connect (hard RST). This is a documented, narrow trade-off — not a blocker for normal `plug -p X <cmd>` trees.

## What exists vs what remains

**Shipped (`cli/internal/tun/pidroute*.go`, `router.go`, `registry_*.go`):** the whole attribution path.
- `walkToCluster(pid, ppidOf, clusterForPID)` — pure, unit-tested ancestry walk (bounded, cycle-safe, refuses on a broken chain, rejects a recycled PID by start-time).
- `multiDial` behind a `clusterRouter` — the single-cluster path is a no-op that always returns its one key, so both cases share one code path.
- `ppidOf`: `/proc/<pid>/stat` (Linux), `ps -o ppid=` (macOS), `CreateToolhelp32Snapshot` (Windows).
- `pidForLocalPort`: `/proc/net/tcp{,6}` inode scan (Linux), `lsof` (macOS), `GetExtendedTcpTable` (Windows).
- On **Windows**, the SYSTEM service holds `map[clusterKey]*tunnel.Transport`, records each launcher PID → cluster in `registry_windows.go`, and routes `handleTCP` through `multiDial` — validated on two live clusters.

**Remaining — macOS only:**
1. **N-tunnel daemon.** Today the macOS daemon holds ONE cluster (keyed `host:port`, one Dialer). Generalize it to hold a `map[clusterKey]*tunnel.Transport` and one shared netstack, exactly as the Windows service already does.
2. **Validate** two `plug -p A` / `plug -p B` trees resolving the same name to different backends concurrently, and a detached child being refused.

## Per-OS gotchas (don't relearn)

- **macOS**: no cgo (`CGO_ENABLED=0` in the image build) → attribution must be cgo-free (`ps`, `lsof`, or `sysctl`), not libproc directly.
- **`/proc/<pid>/stat`**: `comm` can contain spaces and `)` — parse from the **last** `)`.
- **Perf**: cache `PID→cluster` (stable for a process), so the per-connection cost is negligible; only new source ports pay a lookup.
- **Detached processes**: `setsid` breaks the chain → refuse, by design.

## Plan, in order (macOS)

1. Generalize the macOS daemon to N tunnels — mirror the Windows service: a `map[clusterKey]*tunnel.Transport` over one shared netstack.
2. e2e on two clusters: `plug -p A pg` and `plug -p B pg` resolve `postgres` to different backends concurrently; a detached child is refused.
