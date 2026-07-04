# plug hook — transparent connect()/DNS interception (the "N1" layer)

`plug_hook.c` builds a small native shared library that plug injects into the
child process it launches, so that **every outbound TCP connection and DNS lookup
is transparently routed through plug's existing local SOCKS5 proxy** — with no
per-service configuration. It closes the gap left by the env-proxy approach,
which only works for clients that honor `HTTP_PROXY` / `ALL_PROXY` /
`-DsocksProxyHost`. Raw-TCP drivers (Node `amqplib`, `pg`, `mongodb`, `redis`,
gRPC…) ignore those; with the hook loaded they Just Work.

## How it works

The library is injected via `DYLD_INSERT_LIBRARIES` (macOS) / `LD_PRELOAD`
(Linux) and interposes two libc entry points:

- **`getaddrinfo()`** — for a real hostname it returns a synthetic "fake" IPv4
  from a reserved pool (`240.0.0.0/4`, class E, never routed) and records
  `name ↔ fakeIP` in a thread-safe table. The app believes resolution succeeded.
- **`connect()`** — for an outbound TCP connection to a non-loopback address it
  performs an RFC 1928 SOCKS5 handshake to the proxy at `$PLUG_SOCKS` and issues
  a `CONNECT`. If the destination is one of the fake IPs, it recovers the
  original hostname and sends **that** in the SOCKS request, so the cluster's DNS
  resolves it cluster-side (`socks5h` remote resolution). It then splices the
  proxy socket onto the app's fd (via `dup2`) so the app proceeds transparently.

Loopback (`127/8`, `::1`), unix sockets, and non-`SOCK_STREAM` sockets are passed
straight through to the real functions. If `$PLUG_SOCKS` is unset the library is
a transparent no-op. TCP only — UDP / QUIC / ICMP are untouched.

## Platform notes (why the code looks the way it does)

macOS `__interpose` is **total**: once a symbol is in the interpose table, every
runtime lookup of it — `dlsym(RTLD_NEXT)`, `dlsym(RTLD_DEFAULT)`, even a
handle-scoped `dlsym` on the defining dylib — resolves back to our own function
(verified by pointer comparison). So:

- The real **`connect`** is reached with the raw `syscall(SYS_connect, …)` — the
  only path that bypasses the interpose. (Apple deprecates the `syscall()`
  wrapper but it still works and the number comes from the SDK we compile with.)
- The real **`getaddrinfo` is never called**: because interposition is total,
  every `addrinfo` the app ever sees is built by us, so we synthesize it directly
  (fake IP when a proxy is configured; a real IPv4 via the non-interposed
  `gethostbyname` in no-op mode) and free it ourselves.

On Linux the ELF `LD_PRELOAD` model is simpler: we override the symbols and reach
the real `connect` via `dlsym(RTLD_NEXT)`.

## What is NOT covered

- **Go / statically-linked binaries** issue the `connect` syscall directly,
  bypassing libc — the hook never sees them.
- **Apple system binaries** in `/usr/bin`, `/bin`, … are SIP-protected and strip
  `DYLD_*` (so `/usr/bin/curl` is not hooked). Likewise, launching a hooked
  process *through* a SIP-restricted tool (`/usr/bin/perl`, `/usr/bin/env` on some
  setups) strips the injection from the child.
- **macOS binaries with library-validation ON** and no
  `com.apple.security.cs.disable-library-validation` entitlement reject the
  library. Node and the JVM disable it (to load native addons / JNI), so they
  work; many signed third-party apps do not.
- **Non-TCP** traffic (UDP, QUIC/HTTP-3, ICMP).
- **Real IPv6 literals** (the fake pool is IPv4-only; names still work).

For anything above, plug's existing fallbacks remain: `HTTP_PROXY`/`HTTPS_PROXY`,
the JVM `-DsocksProxyHost`, and explicit per-service `forward = ENV=url`.

## Building

```sh
make            # macOS arm64 (the dev machine) → build/darwin-arm64/plug-hook.dylib
make darwin-universal
make linux-amd64 linux-arm64   # on a Linux host / with a linux-targeting clang
```

## License

`plug_hook.c` is **original code**, Apache-2.0 (same as plug). Its *design* — the
proxychains model of interposing `connect()` + `getaddrinfo()` with a fake-IP
table for remote DNS — is well known, but **no proxychains-ng source was copied**;
proxychains-ng is GPL-2.0, incompatible with plug's Apache-2.0. This file was
written from scratch against RFC 1928 and the POSIX / dyld interposition APIs.
