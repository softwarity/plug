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
  3. service (SYSTEM)      StartGlobalDatapath: WinTUN + netstack + NRPT (*.plug)
                           reconcile: open a tunnel per ActiveClusters()  → mark X ready
  4. waitClusterReady(X)   then run <cmd>; its flows hit the WinTUN, are attributed to X
                           by PID at connect, spliced down X's tunnel
  reaper                   stops the datapath 30 s after the last client of any cluster exits
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
| `agent/install.ps1` | self-elevates `plug install-service` once |

## Status

- ✅ **Build-validated** darwin / linux / windows; **mac tests green** (multicluster mac
  untouched).
- ⚠️ **Runtime NOT validated** — no Windows here. Everything below is to check with
  Sylvain.

## Test plan (tomorrow, on Sylvain's Windows)

Rebuild the image from HEAD, update the agent, then:

1. **Install** (Git Bash): the one-liner now self-elevates the service —
   expect a single UAC prompt, then `datapath service installed.`
   - Check: `Get-Service plug` → `Stopped` (on-demand), and
     `sc.exe qc plug` shows `binPath = "...\plug.exe" __plug-daemon`, `START_TYPE = DEMAND`.
2. **No-admin run** (a **normal**, non-elevated terminal):
   `plug curl http://user-mng-frontend:8081/user-mng-frontend/en/index.html`
   - Expect: service starts (`Get-Service plug` → `Running`), the page resolves and
     returns 200. Service log: `%ProgramData%\plug\service.log`.
3. **Multicluster**: with two agents (as we did on mac), run
   `plug -p a <cmd>` ‖ `plug -p b <cmd>` from two normal terminals — each must reach
   only its own cluster.
4. **Teardown**: after the last `plug` exits, ~30 s later `Get-Service plug` → `Stopped`.
   `plug down` stops it now.

## Known risk points (where to look first if it fails)

- **Service ACL** — if a non-elevated `startService()` fails with access-denied, the
  SDDL in `installService` needs adjusting (`sc.exe sdget plug` to inspect).
- **DNS/WinTUN in session 0** — the service runs in session 0; WinTUN + NRPT are
  machine-wide so should apply, but the NRPT `Clear-DnsClientCache` may behave
  differently from a service (verify a fresh `plug` resolves).
- **Service binary vs launcher version** — the service points at the installed
  `plug.exe`; if the launcher auto-updates to a newer version, the service is stale.
  Re-run `plug install-service` after a version bump (or we teach the launcher to
  refresh it). Not wired yet.
- **`Execute` shutdown ordering** — confirm StopPending→Stopped is reported promptly so
  the SCM doesn't mark the service hung.
