# Third-party licenses

plug is licensed under **FSL-1.1-Apache-2.0** (see [`LICENSE`](LICENSE)). It
bundles the third-party components below, each under **its own license** —
listed here for attribution. Full license texts are at the linked sources (and
are vendored with each Go module). Nothing here changes plug's own
FSL-1.1-Apache-2.0 terms.

The Go-module list is generated from the actual link graph of the `plug` binary
(`go-licenses`, union of linux / macOS / windows builds).

## plug CLI — statically linked into the `plug` binary

| Component | Role | License |
|---|---|---|
| The Go standard library & toolchain | one static binary per platform | BSD-3-Clause |
| `golang.org/x/crypto` | SSH client - the in-process `direct-tcpip` transport | BSD-3-Clause |
| `golang.org/x/net` | networking helpers | BSD-3-Clause |
| `golang.org/x/sys` | OS syscalls (unix / windows) | BSD-3-Clause |
| `golang.org/x/time` | rate limiting | BSD-3-Clause |
| `golang.zx2c4.com/wireguard` (wireguard-go) | the userspace TUN device | MIT |
| `golang.zx2c4.com/wintun` | Windows TUN wrapper | MIT |
| `gvisor.dev/gvisor` | userspace network stack (netstack) that answers DNS and terminates flows | Apache-2.0 |
| `github.com/google/btree` | data structure used by the netstack | Apache-2.0 |

Sources / copyright holders / full texts:

- **Go, `x/crypto`, `x/net`, `x/sys`, `x/time`** — BSD-3-Clause, © The Go Authors — <https://cs.opensource.google/go>
- **wireguard-go, wintun** — MIT, © Jason A. Donenfeld / WireGuard LLC — <https://git.zx2c4.com/wireguard-go>
- **gVisor** — Apache-2.0, © The gVisor Authors — <https://github.com/google/gvisor/blob/master/LICENSE>
- **btree** — Apache-2.0, © Google — <https://github.com/google/btree/blob/master/LICENSE>

## plug agent — the `docker.io/softwarity/plug` image

The agent is an Alpine image bundling these **independent programs**. This is
*mere aggregation* — each keeps its own license, and plug's FSL-1.1-Apache-2.0
covers only the `plug-agent` binary plug adds, not the programs below.

| Component | Role | License |
|---|---|---|
| Alpine Linux (base image) | minimal userland | MIT (+ per-package) |
| `golang.org/x/crypto` | SSH **server** - the agent's own, doing the `direct-tcpip` dials and the remote forwards | BSD-3-Clause |
| musl libc | C library | MIT |
| BusyBox | shell / coreutils | GPL-2.0 |

Sources:

- **Alpine Linux** — <https://www.alpinelinux.org>
- **musl** — <https://musl.libc.org>
- **BusyBox** — <https://busybox.net>

---

None of these licenses restrict redistribution alongside a source-available
license like FSL: BSD, MIT and Apache-2.0 are permissive (no obligation flows
back onto plug), and BusyBox's GPL-2.0 only reaches the aggregated image, not
plug's own code — the image programs run as separate processes (mere
aggregation), not linked into plug's code.
