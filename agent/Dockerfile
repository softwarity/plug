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

FROM alpine:3.21

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
# The Windows PowerShell installer, streamed by serve-binary on `install-windows`.
# MUST come AFTER the COPYs above: a `--chmod=644` COPY that is the FIRST to create
# /opt/plug applies 644 to the directory itself, dropping its +x so the unprivileged
# `get` user can't traverse it — which breaks serving VERSION and the binaries. By
# now /opt/plug already exists as 0755, so this only sets install.ps1's own mode.
COPY --chmod=644 agent/install.ps1 /opt/plug/install.ps1

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
