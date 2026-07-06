# Multicluster — design (PID-at-connect)

Status: **design validated, attribution core landed (`cli/internal/tun/pidroute*.go`), not yet wired into the datapath.** The single-cluster path is unchanged and stays the proven one until this is validated on two real clusters.

## The problem

Running two *different* clusters at once on the same machine.

- **Linux is already there.** Each `plug` launch gets a private `/etc/resolv.conf` bind-mounted in its own mount namespace, so two launches never share DNS — `plug -p A` and `plug -p B` are isolated for free.
- **macOS / Windows are the hard case.** There is no per-process resolver: plug repoints the **system** resolver (macOS `scutil` dynamic store, Windows adapter DNS), which is **machine-wide**. Two clusters would fight over one resolver. That is why 1.1.0 documents "one active cluster at a time" on macOS.

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

**Landed now (`cli/internal/tun/pidroute*.go`):** the attribution core.
- `walkToCluster(pid, ppidOf, clusterForPID)` — pure, unit-tested ancestry walk (bounded, cycle-safe, refuses on a broken chain).
- `pidRouter` / `staticRouter` behind a `clusterRouter` interface — the single-cluster router is a no-op that always returns its one key, so wiring it later is a **zero-regression** change.
- `ppidOf`: real on Linux (`/proc/<pid>/stat`) and macOS (`ps -o ppid=`), stub on Windows.
- `pidForLocalPort`: real on Linux (`/proc/net/tcp{,6}` inode → `/proc/<pid>/fd` scan), stub on macOS/Windows.

**Remaining (needs two real clusters to validate):**
1. **N-tunnel daemon.** Today the macOS daemon holds ONE cluster (keyed `host:port`, one `tr Dialer`). Generalize it to hold a `map[clusterKey]*tunnel.Transport` and one shared netstack.
2. **`clusterForPID` registry.** Record each `plug -p X` launcher PID → cluster key as clients register (extend `registry_darwin.go`, add the other OSes).
3. **Wire `handleTCP`.** Replace the single `tr Dialer` with a `clusterRouter`: `route(srcPort)` → cluster → `dialers[cluster].DialCluster(name:port)`; refuse (RST) when `ok==false`. Keep `staticRouter` for the single-cluster case.
4. **`pidForLocalPort` on macOS** (`lsof -nP -iTCP:<port>` parse, or `net.inet.tcp.pcblist` sysctl) **and Windows** (`GetExtendedTcpTable`, `MIB_TCPTABLE_OWNER_PID`).
5. **`ppidOf` on Windows** (`CreateToolhelp32Snapshot` → `th32ParentProcessID`).

## Per-OS gotchas (don't relearn)

- **macOS**: no cgo (`CGO_ENABLED=0` in the image build) → attribution must be cgo-free (`ps`, `lsof`, or `sysctl`), not libproc directly.
- **`/proc/<pid>/stat`**: `comm` can contain spaces and `)` — parse from the **last** `)`.
- **Perf**: cache `PID→cluster` (stable for a process), so the per-connection cost is negligible; only new source ports pay a lookup.
- **Detached processes**: `setsid` breaks the chain → refuse, by design.

## Plan, in order

1. N-tunnel daemon skeleton + `clusterForPID` registry (macOS first — it's the resolver-global case).
2. Wire `handleTCP` behind `clusterRouter` (`staticRouter` default → no behaviour change until ≥2 clusters).
3. `pidForLocalPort`/`ppidOf` for macOS, then Windows.
4. e2e on two clusters: `plug -p A pg` and `plug -p B pg` resolve `postgres` to different backends concurrently; a detached child is refused.
