# Windows: the datapath SYSTEM service (no-admin + multicluster)

On macOS the global datapath lives in a **detached daemon**; on Windows it lives in
an **SCM service**. Same design (`docs/multicluster.md`), different process model.
This one component solves **both** remaining Windows gaps at once:

- **No admin day-to-day** — the privileged work (WinTUN + routes + NRPT DNS) is in the
  service (SYSTEM); a non-elevated `plug <cmd>` just talks to it.
- **Multicluster** — the service holds **one tunnel per active cluster** and
  attributes each flow to a cluster **by PID at connect** (the bricks in
  `pidroute_windows.go`, hardened against PID recycling).

## How it flows

```
install (ONE UAC)         plug install-service  ──► SCM: create service "plug"
                                                     binPath = plug.exe __plug-daemon
                                                     StartType = demand (on-demand)
                                                     SDDL grants Authenticated Users START/STOP

plug -p X <cmd>  (NO admin)
  1. RegisterClient(X)     drop a marker in %ProgramData%\plug\<hash>.clients\<pid>  (carries X)
  2. startService()        SCM starts "plug" if not running  (users may, per the SDDL)
  3. service (SYSTEM)      StartGlobalDatapath: WinTUN + netstack + NRPT (*.plug, via registry)
                           reconcile (~0.3s): open a tunnel per ActiveClusters()  → mark X ready
  4. waitClusterReady(X)   then run <cmd>; its flows hit the WinTUN, are attributed to X
                           by PID at connect, spliced down X's tunnel
  grace                    an idle cluster tunnel is kept 20 s so back-to-back runs reuse it
  reaper                   stops the whole datapath 30 s after the last client of any cluster exits
```

**Fallback:** if the service is **not installed**, `coreRun` holds the datapath in
**this (elevated) process** — the original single-cluster path, validated first. So
`plug` always works, service or not; the service only *removes the admin need* and
*adds multicluster*.

## Files

| File | Role |
|---|---|
| `cli/daemon_windows.go` | the `svc.Run` service (`plugService.Execute`), `startService`, `cmdDown`, `installService`/`removeService` |
| `cli/socks_run_windows.go` | `coreRun`: service-delegate path + in-process fallback |
| `cli/daemon_shared.go` | portable reconcile loop (shared darwin‖windows) |
| `cli/internal/tun/registry_windows.go` | client registry = the launcher↔service IPC (file markers), `processAlive` via OpenProcess |
| `cli/internal/tun/graft_windows.go` | `graftDir` (ProgramData), ready markers, `DaemonAlive` (service query), leader = SCM |
| `cli/internal/tun/router.go` | `multiDial` (PID→cluster routing), shared darwin‖windows |
| `agent/install.sh` | Git Bash installer: downloads plug.exe + wintun.dll from the agent, sets PATH + profile, then `plug install-service` when elevated |

## Status — validated end-to-end on a real Windows 11 box

- ✅ **Install** (elevated Git Bash): downloads plug.exe + wintun.dll from the agent, sets
  PATH + profile, installs the service. `Get-Service plug` → `Stopped` (on-demand);
  `sc.exe qc plug` → `binPath "...\plug.exe __plug-daemon"`, `START_TYPE = DEMAND`;
  `sc.exe sdshow plug` grants Authenticated Users START/STOP.
- ✅ **No-admin run** — from a genuinely non-elevated process (a `schtasks /rl LIMITED`
  task = filtered token): the service starts (`Stopped → Running`) and the single-label
  name resolves, returns 200.
- ✅ **Multicluster** — two live clusters (neo + llm) reached side by side, each flow
  attributed to its own cluster by name; plus 3 concurrent same-cluster sessions.
- ✅ **Teardown** — datapath stops ~30 s after the last client; `plug down` / stop now.
- ✅ **Versioning** — a version mismatch triggers a real download from the agent
  (plug.exe + wintun.dll beside it) over crypto/ssh, then runs.
- ✅ **Cold-start ~0.8 s** — NRPT via the registry (no PowerShell), tunnel opened in ~0.3 s,
  a 20 s per-cluster grace so back-to-back runs reuse the tunnel.

## Notes learned on the way (all fixed)

- **Version probe/download over the external ssh binary hangs on Windows** when stdout is
  captured over a pipe (OpenSSH forks a child that holds the pipe's write end open, so EOF
  never arrives) → routed through the crypto/ssh library instead, like the data tunnel.
- **Concurrent sessions** — a channel the agent *rejects* (bare names Windows probes, e.g.
  WPAD) must not trigger a reconnect: it used to tear down the shared SSH connection and
  kill every other in-flight session. Only rejections that are genuine connection errors
  reconnect now (cross-platform fix).
- **Host-key TOFU** pins in `%ProgramData%\plug\known_hosts` (user-writable), not the
  service's `%SystemProfile%`, so a changed key can be reset without admin.
- **NRPT** is written straight to the `DnsPolicyConfig` registry + `DnsFlushResolverCache`
  (no PowerShell), and routes only `.plug` — short local names are untouched between runs.
- **Service binary vs launcher version** — the service points at the installed `plug.exe`;
  after a version bump, re-run `plug install-service` (auto-refresh is still a TODO).
