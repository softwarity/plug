# Build context is the REPO ROOT (so the CLI sources are available):
#   docker build -f agent/Dockerfile -t softwarity/plug .
# The version — VERSION plus the GIT_REV build metadata — is stamped into every
# binary and written to /opt/plug/VERSION; the CLI compares against it to decide
# whether to fetch a newer core, so even rebuilds of `latest` (same VERSION, new
# commit) differ. Pass the revision: --build-arg GIT_REV=$(git rev-parse --short HEAD)
ARG VERSION=dev
ARG GIT_REV=

FROM golang:1.26-alpine AS build
ARG VERSION
ARG GIT_REV
# plug is a single self-contained Go binary — its data path is the userspace TUN
# (gVisor netstack + wireguard-go device), no C hook or seccomp supervisor to
# cross-build anymore.
WORKDIR /src
COPY cli/ ./cli/
WORKDIR /src/cli
RUN mkdir -p /out/bin && \
    if [ -n "${GIT_REV}" ]; then V="${VERSION}+${GIT_REV}"; else V="${VERSION}"; fi && \
    for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
      os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=$V" \
        -o "/out/bin/plug-$os-$arch$ext" .; \
    done && \
    printf '%s' "$V" > /out/VERSION

# The agent-side helper: ForceCommand of the tunnel user (the -s provisioning
# verbs + the signpost relay + boot gc). Static, tiny, stdlib-only.
COPY agent/helper/ /src/helper/
RUN CGO_ENABLED=0 go build -C /src/helper -trimpath -ldflags "-s -w" -o /out/plug-agent .

# WinTUN driver DLL, fetched once at build so the agent serves it to the Windows
# installer (the installer pulls it from us — no run-time dependency on wintun.net,
# and no more intermittent fetch).
ARG WINTUN_VERSION=0.14.1
RUN apk add --no-cache curl unzip && \
    curl -fsSL "https://www.wintun.net/builds/wintun-${WINTUN_VERSION}.zip" -o /tmp/wintun.zip && \
    unzip -j /tmp/wintun.zip 'wintun/bin/amd64/wintun.dll' -d /out

FROM alpine:3.24

# openssh only: the CLI tunnels via SSH direct-tcpip, sshd resolves cluster
# names and forwards connections itself — no other server-side tooling needed.
RUN apk add --no-cache openssh-server \
    && adduser -D -s /bin/sh plug \
    && sed -i 's/^plug:!/plug:*/' /etc/shadow \
    # anonymous download user: empty password, locked to the serve-binary ForceCommand
    && adduser -D -s /bin/sh get \
    && passwd -u get 2>/dev/null; sed -i 's/^get:!/get:*/' /etc/shadow \
    && { \
         echo 'PermitRootLogin no'; \
         echo 'AllowUsers plug get'; \
         echo 'Match User plug'; \
         echo '  PasswordAuthentication no'; \
         echo '  AllowTcpForwarding yes'; \
         echo '  PermitOpen any'; \
         # the tunnel user's whole exec surface is the -s helper: serve-name /
         # unserve-name, no shell (direct-tcpip and remote forwards are channels,
         # not sessions — ForceCommand does not touch them)
         echo '  ForceCommand /usr/local/bin/plug-agent'; \
         # expose (reverse direction): honour the CLI's 0.0.0.0 remote-forward bind
         # so other workloads reach it via the agent's network alias — without this
         # sshd silently binds the loopback and the exposed port answers nobody.
         echo '  GatewayPorts clientspecified'; \
         # detect dead tunnels server-side so their remote-forward binds free up
         # fast enough for a reconnecting session to re-arm (~90s worst case).
         echo '  ClientAliveInterval 30'; \
         echo '  ClientAliveCountMax 3'; \
         echo 'Match User get'; \
         echo '  PermitEmptyPasswords yes'; \
         echo '  PasswordAuthentication yes'; \
         echo '  AuthenticationMethods none'; \
         echo '  ForceCommand /usr/local/bin/serve-binary'; \
         echo '  AllowTcpForwarding no'; \
         echo '  X11Forwarding no'; \
       } >> /etc/ssh/sshd_config

COPY --chown=plug:plug --chmod=600 agent/authorized_keys /home/plug/.ssh/authorized_keys
RUN chmod 700 /home/plug/.ssh
COPY --chmod=755 agent/serve-binary /usr/local/bin/serve-binary
COPY --chmod=755 agent/entrypoint.sh /entrypoint.sh
COPY --from=build /out/bin/ /opt/plug/bin/
COPY --from=build /out/VERSION /opt/plug/VERSION
# setuid root: the helper runs as the `plug` SSH user but needs the docker
# sock / the pod's SA token. Its whole reachable surface is the ForceCommand
# dispatch — two verbs, DNS-label-validated args, no shell (same pattern as the
# CLI's macOS setuid helper).
COPY --from=build --chmod=4755 /out/plug-agent /usr/local/bin/plug-agent
# The WinTUN DLL (served on the `wintun` verb) and the Git Bash installer (served
# on `install-windows`). MUST come AFTER the COPYs above: a COPY that is the FIRST
# to create /opt/plug would apply its mode to the directory itself, dropping the +x
# the unprivileged `get` user needs to traverse it. By now /opt/plug is already
# 0755, so these only set their own files' modes.
COPY --from=build /out/wintun.dll /opt/plug/wintun.dll
COPY --chmod=644 agent/install.sh /opt/plug/install.sh

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
