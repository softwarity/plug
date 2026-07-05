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
- **CASE 1** (required) — libc (`curl`) reaches `web` by name → **PASS**.
- **CASE 2** (xfail on Linux) — Go raw-TCP reaches `web` by name → **flips to PASS**
  when the seccomp supervisor lands (Go bypasses libc + the pure-Go resolver).

## Add a case

One client (any language) + one assertion block in `run-cases.sh`. Required
cases must PASS (they fail the suite); known gaps are XFAIL (warn only). The
matrix here IS the "don't forget a case" checklist.
