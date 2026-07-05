# e2e — full-path regression tests

Stands up a mini-cluster in Docker and runs client apps **under plug** against
it, asserting cluster services are reachable **by name**. Runs locally and in CI
(`.github/workflows/e2e.yml` — free on this public repo, on every push/PR).

This is the regression guard: if a change breaks a covered case, the suite goes
red **before** you retest by hand.

## Run

    bash e2e/run.sh      # builds the agent from source, brings it up, asserts, tears down

## Topology

- `client` (edge net) — the "laptop": sees `agent`, **not** `web`.
- `agent`  (edge + internal) — the plug agent (sshd + hook binaries).
- `web`    (internal) — a cluster service, reachable **only** through plug.

## Cases (the checklist)

- **CONTROL** — curl `web` without plug → unreachable.
- **CASE 1** (required) — libc (`curl`) reaches `web` by name → **PASS** (the
  preload hook).
- **CASE 2** (required) — Go raw-TCP reaches `web` by name → **PASS** (the seccomp
  supervisor; Go bypasses libc for both the pure-Go resolver and connect).

> CASE 2 needs, **inside a container only**, `seccomp:unconfined` + `SYS_PTRACE`
> on the client (see `docker-compose.yml`) so the supervisor can install its
> seccomp user-notifier and `process_vm_readv` its child. On a real host neither
> is required.

## Add a case

One client (any language) + one assertion block in `run-cases.sh`. Required
cases must PASS (they fail the suite); known gaps are XFAIL (warn only). The
matrix here IS the "don't forget a case" checklist.
