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
| `golang.org/x/term` | terminal detection, so a prompt is only shown to someone who can answer it | BSD-3-Clause |
| `golang.org/x/exp` | generic constraints, pulled in by the netstack's buffer and bits packages | BSD-3-Clause |
| `golang.org/x/time` | rate limiting | BSD-3-Clause |
| `golang.zx2c4.com/wireguard` (wireguard-go) | the userspace TUN device | MIT |
| `golang.zx2c4.com/wintun` | Windows TUN wrapper | MIT |
| `golang.zx2c4.com/wireguard/windows` | Windows IP Helper bindings (`tunnel/winipcfg`), used to set routes and DNS | MIT |
| `gvisor.dev/gvisor` | userspace network stack (netstack) that answers DNS and terminates flows | Apache-2.0 |
| `github.com/google/btree` | data structure used by the netstack | Apache-2.0 |
| `github.com/modelcontextprotocol/go-sdk` | `plug mcp`: the MCP server an AI coding agent talks to | Apache-2.0 (with MIT-licensed contributions, per its LICENSE) |
| `github.com/google/jsonschema-go` | JSON Schema for the MCP tools' inputs and outputs | MIT |
| `github.com/segmentio/encoding` | JSON codec the MCP SDK uses | MIT |
| `github.com/segmentio/asm` | assembly helpers behind that codec | MIT-0 |
| `github.com/yosida95/uritemplate/v3` | URI templates, MCP resource URIs | BSD-3-Clause |
| `golang.org/x/sync` | errgroup, pulled in by the MCP SDK | BSD-3-Clause |
| `golang.org/x/oauth2` | OAuth2 types the MCP SDK's auth helpers declare (unused over stdio) | BSD-3-Clause |

Sources / copyright holders / full texts:

- **Go, `x/crypto`, `x/exp`, `x/net`, `x/sys`, `x/term`, `x/time`**: BSD-3-Clause, © The Go Authors, <https://cs.opensource.google/go>
- **wireguard-go, wintun, wireguard-windows**: MIT, © Jason A. Donenfeld / WireGuard LLC, <https://git.zx2c4.com/wireguard-go>
- **gVisor** — Apache-2.0, © The gVisor Authors — <https://github.com/google/gvisor/blob/master/LICENSE>
- **btree** — Apache-2.0, © Google — <https://github.com/google/btree/blob/master/LICENSE>

## plug agent — the `docker.io/softwarity/plug` image

The agent image is the `plug-agent` binary on a distroless base, which carries
no program at all: no shell, no libc, no package manager. Only data files, each
under its own license. This is *mere aggregation*: plug's FSL-1.1-Apache-2.0
covers only the `plug-agent` binary plug adds, not the files below.

| Component | Role | License |
|---|---|---|
| distroless `static-debian13` (base image) | the image's skeleton | Apache-2.0 |
| `golang.org/x/crypto` | SSH **server** - the agent's own, doing the `direct-tcpip` dials and the remote forwards | BSD-3-Clause |
| Debian `ca-certificates` | the root certificates the agent's HTTPS requests trust | MPL-2.0 |
| Debian `base-files`, `netbase`, `media-types`, `tzdata` | `/etc` skeleton, protocol and service names, MIME types, time zones | GPL-2.0+ · public domain |

Sources:

- **distroless** - <https://github.com/GoogleContainerTools/distroless>
- **Debian** - <https://sources.debian.org>

---

None of these licenses restrict redistribution alongside a source-available
license like FSL: BSD, MIT and Apache-2.0 are permissive (no obligation flows
back onto plug), and the GPL of Debian's base files only reaches the aggregated
image, not plug's own code: they are data files beside it (mere aggregation),
not linked into plug's code.
