# go-pg — test subject for plug's Go coverage

A tiny **Go** binary that connects to Postgres and runs `select version()`.
Purpose: prove whether plug intercepts a Go binary's outbound connection.

Go bypasses libc on Linux (raw syscalls) → the current LD_PRELOAD hook can't see
its `connect()`. This is exactly the case we want to close (seccomp-unotify).

## Setup

    cp .env.example .env      # then fill in a CLUSTER-INTERNAL postgres
                              # (PG_HOST = a service name like `odb`)

## Run

    go build -o goclient .

    ./goclient            # WITHOUT plug → should FAIL (odb doesn't resolve locally)
    plug ./goclient       # WITH plug    → succeeds IFF the connect() is intercepted

## What each platform tells us

- **macOS**: Go goes through libSystem → `plug ./goclient` also tests whether the
  current DYLD hook already catches Go (it might!).
- **Linux**: Go makes raw syscalls → expected to FAIL with the `.so` hook,
  which motivates the seccomp-unotify supervisor.
