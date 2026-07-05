# Embedded seccomp supervisor

plug embeds the compiled Go-coverage supervisor here (via go:embed), writes it to
`~/.plug/lib/` at runtime, and wraps the child with it on Linux. The binary is
named `plug-seccomp-linux-<arch>`.

It is produced by the agent Dockerfile with `zig cc` (glibc target, both
arches), never committed — this directory only ships a README so the embed never
fails on a build where no binary was placed (e.g. a plain `go build` on macOS,
where the supervisor is irrelevant). When it is absent, `seccomp.Available()`
returns false and plug runs the child unwrapped — libc apps stay covered by the
preload hook; Go/static apps fall back to the port-forwards.

See `csrc/plug_seccomp.c` for what the supervisor does.
