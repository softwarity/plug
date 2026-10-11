# Build context is the REPO ROOT (so the CLI sources are available):
#   docker build -f agent/Dockerfile -t softwarity/plug .
# The version — VERSION, plus GIT_REV as build metadata when one is given — is
# stamped into every binary and written to /opt/plug/VERSION; the CLI compares
# against it to decide whether to fetch a newer core. Pass GIT_REV for a build
# off a MOVING ref (a branch, where VERSION is "dev"), so two rebuilds of the
# same tag stay distinct: --build-arg GIT_REV=$(git rev-parse --short HEAD).
# A release passes none: v2.4.0 already designates exactly one commit.
ARG VERSION=dev
ARG GIT_REV=
# FLAVOUR names which client this image serves, and it rides in the VERSION
# STRING rather than in a separate stamp. That string is already the cache key
# for the core a launcher fetches and the value the agent announces, so two
# flavours can never collide on one cached binary, and nothing can disagree with
# anything. Empty is the standalone product; `hosted` is the client for a gateway
# that embeds the agent (Meerkat), which carries keygen/pubkey and no update.
#   docker build --build-arg FLAVOUR=hosted -t softwarity/plug:hosted .
ARG FLAVOUR=

# On the BUILD platform, whatever the target: this stage is a Go cross-compile
# (the six CLI binaries already are, and the agent below asks for TARGETARCH),
# so emulating the target here would only make the compiler run under QEMU.
# That is what lets one job build both architectures at release time, where
# the final stage runs nothing that has to be emulated.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG VERSION
ARG GIT_REV
ARG FLAVOUR
ARG TARGETARCH
# plug is a single self-contained Go binary — its data path is the userspace TUN
# (gVisor netstack + wireguard-go device), no C hook or seccomp supervisor to
# cross-build anymore.
WORKDIR /src
COPY cli/ ./cli/
WORKDIR /src/cli
RUN mkdir -p /out/bin && \
    if [ -n "${GIT_REV}" ]; then V="${VERSION}+${GIT_REV}"; else V="${VERSION}"; fi && \
    if [ -n "${FLAVOUR}" ]; then V="${V}-${FLAVOUR}"; fi && \
    for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
      os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=$V" \
        -o "/out/bin/plug-$os-$arch$ext" .; \
    done && \
    printf '%s' "$V" > /out/VERSION

# WinTUN driver DLL, fetched once at build so the agent serves it to the Windows
# installer (the installer pulls it from us — no run-time dependency on wintun.net,
# and no more intermittent fetch).
ARG WINTUN_VERSION=0.14.1
# The archive's SHA-256, checked before anything is unpacked from it.
#
# This DLL is loaded by a process running elevated on Windows: the datapath. It
# was fetched over TLS and unpacked on faith, so whoever could answer for
# wintun.net during a build - a compromise there, a poisoned mirror, a proxy in
# the way - put code into that process. TLS says who answered, never what they
# said.
#
# Be exact about what the pin proves and what it does not. It was taken from the
# archive this build fetched, so it does not certify that upstream published
# these bytes; it certifies that the bytes never CHANGE without someone editing
# this line. That is the property that matters here: 0.14.1 names one release,
# so a different digest for it is a substitution, and the build stops instead of
# shipping it. Bumping WINTUN_VERSION means bumping this too, deliberately.
ARG WINTUN_SHA256=07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51
RUN for i in 1 2 3; do apk add --no-cache curl unzip && break; \
      echo "apk failed (attempt $i/3) — retrying"; sleep $((i * 5)); \
    done; [ -x /usr/bin/curl ] && \
    curl -fsSL --retry 3 --retry-connrefused "https://www.wintun.net/builds/wintun-${WINTUN_VERSION}.zip" -o /tmp/wintun.zip && \
    echo "${WINTUN_SHA256}  /tmp/wintun.zip" | sha256sum -c - && \
    unzip -j /tmp/wintun.zip 'wintun/bin/amd64/wintun.dll' -d /out && \
    mv /out/wintun.dll /out/wintun-amd64.dll && \
    unzip -j /tmp/wintun.zip 'wintun/bin/arm64/wintun.dll' -d /out && \
    mv /out/wintun.dll /out/wintun-arm64.dll

# Sign what this image will serve. The launcher runs the core with the privilege
# it holds (root on macOS, CAP_SYS_ADMIN on Linux) and the agent it fetches from
# is chosen by whoever runs plug, so a digest announced by that same agent proves
# nothing. Only a signature made with a key the caller cannot reach does, and
# that key lives here for the length of this one RUN: a BuildKit secret mount is
# a tmpfs, it lands in no layer and no image history.
#
# No key mounted is the normal case for a local `docker build` or a fork, and it
# must not fail the build: the image simply serves no sig=, and a launcher
# refuses to install anything from it. What the release
# pipeline must never do is silently take that path, so the message says which
# one was taken and CI greps for it.
#
# The WinTUN DLLs are signed in the same run (fetched above for that reason):
# they are loaded by the SYSTEM service on Windows, and the launcher now admits
# them through the same attestation as the binaries. They are then staged under
# the names the agent serves them by, each .sig beside its file, so the final
# stage copies them in one glob whether or not a key was mounted.
RUN --mount=type=secret,id=release_key \
    if [ -s /run/secrets/release_key ]; then \
      go run ./cmd/plug-sign /run/secrets/release_key /out/bin "$(cat /out/VERSION)" \
        /out/wintun-amd64.dll /out/wintun-arm64.dll; \
    else \
      echo "plug-sign: NO RELEASE KEY MOUNTED, this image serves UNSIGNED binaries"; \
    fi && \
    mkdir -p /out/wintun && \
    cp /out/wintun-amd64.dll /out/wintun/wintun.dll && \
    cp /out/wintun-arm64.dll /out/wintun/wintun-arm64.dll && \
    { [ ! -f /out/wintun-amd64.dll.sig ] || cp /out/wintun-amd64.dll.sig /out/wintun/wintun.dll.sig; } && \
    { [ ! -f /out/wintun-arm64.dll.sig ] || cp /out/wintun-arm64.dll.sig /out/wintun/wintun-arm64.dll.sig; }

# The agent: its SSH server, the -s provisioning verbs, the signpost relay, the
# boot gc and the anonymous download account. Copied file by file rather than
# as a directory, because the Go sources sit beside the image files
# (Dockerfile, install.sh) so that the module path is
# github.com/softwarity/plug/agent - what Meerkat imports when it links the
# agent into its own binary. install-unix.sh is Go's too: the installer the
# download account emits, embedded in the binary.
COPY agent/*.go agent/go.mod agent/go.sum agent/install-unix.sh /src/agent/
COPY agent/cmd/ /src/agent/cmd/
# ./cmd/plug-agent, not "." - the package is importable now (Meerkat links it
# into its own binary), so the main lives in a cmd of its own.
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build -C /src/agent -trimpath -ldflags "-s -w" -o /out/plug-agent ./cmd/plug-agent

# Everything the image holds beyond its base, laid out HERE with its modes: the
# final stage has no shell, so no mkdir or chmod can run there, and a COPY
# --from keeps the modes it finds. Owned by root (COPY gives uid and gid 0).
#
# /opt/plug holds ONLY what this agent hands out: the client binaries, the
# version they answer to, the Windows installer and the WinTUN DLLs, each .sig
# beside its file. Nothing that is this agent's own running state, because an
# embedder copies this directory wholesale into its image, and admission keys
# or a host key travelling there are read as meaningful when they are not: a
# gateway takes its identity from its vault and its admissions from its
# database.
#
# The amd64 DLL keeps the unsuffixed name, and that is a compatibility decision
# rather than tidiness: the `wintun` verb has always answered one file, and
# every Windows launcher already installed asks for it by that name. It must
# keep getting an amd64 DLL, or an update would hand an arm64 driver to an
# amd64 machine and break a datapath that was working.
#
# /var/lib/plug is the standalone agent's own state, outside what is handed
# out: the keys it admits, and what must outlive a process restart (the host
# key today). Not a volume: a pod that is recreated legitimately starts fresh.
# state/ may be written by group 0, the OpenShift convention: a pod admitted
# under the restricted SCC runs as a uid of the namespace's range, unknown to
# the image, and always in group 0.
COPY agent/authorized_keys agent/install.sh /src/image/
RUN mkdir -p /rootfs/opt/plug /rootfs/var/lib/plug/state /rootfs/usr/local/bin && \
    cp -R /out/bin /rootfs/opt/plug/bin && \
    cp /out/VERSION /out/wintun/* /src/image/install.sh /rootfs/opt/plug/ && \
    cp /src/image/authorized_keys /rootfs/var/lib/plug/authorized_keys && \
    cp /out/plug-agent /rootfs/usr/local/bin/plug-agent && \
    chmod -R a+rX,go-w /rootfs/opt/plug /rootfs/var/lib/plug && \
    chmod 755 /rootfs/usr/local/bin/plug-agent && \
    chmod 770 /rootfs/var/lib/plug/state

# No shell, no package manager, no libc: plug-agent is a static Go binary and
# runs nothing of the system's. What the base brings is what Go reads, the root
# certificates (the agent asks the registry over HTTPS for updates;
# SSL_CERT_FILE and SSL_CERT_DIR add a company's own) and a /tmp with its
# sticky bit, where the name leases live. Root by default, as the agent must be
# to reach the docker socket on Compose and Swarm; Kubernetes and OpenShift run
# it as whatever uid they choose.
#
# The anonymous download account was the last reason for a shell here: a /bin/sh
# script answered it. It is plug-agent now (download.go), so a command from
# whoever reaches that account has no interpreter left to reach.
FROM gcr.io/distroless/static-debian13

# A live mount (--mount) needs no package here: the agent starts a HELPER
# beside the workload - this very image, running `plug-agent mount-serve` with
# the workload's volume or PVC mounted - and the SMB server is compiled into
# plug-agent (github.com/softwarity/smbserver). The developer's process reads
# and writes the real data at the real path through the SMB client every OS
# already has (mount_smbfs, the cifs module, the Windows redirector). Nothing
# to install on the workstation, no second image to pull or to mirror, and
# nothing for an embedder to add to its own image: the helper is the agent, as
# the signpost is.

# No sshd. The SSH server is `plug-agent serve` (agent/sshserver.go), which
# speaks the same protocol to the same CLI: two accounts, publickey for the
# tunnel and none for downloads, no shell for either, direct-tcpip and remote
# forwards binding the address the client names. plug-agent is not setuid: the
# server process holds the docker socket or the pod's SA token directly.
COPY --from=build /rootfs/opt/plug /opt/plug
COPY --from=build /rootfs/var/lib/plug /var/lib/plug
COPY --from=build /rootfs/usr/local/bin/plug-agent /usr/local/bin/plug-agent

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/plug-agent", "serve"]
