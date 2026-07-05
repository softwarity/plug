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
# zig cross-compiles the Linux hook lib (C) for both arches, targeting glibc —
# the developer's runtime is almost always glibc, and a musl .so would not
# LD_PRELOAD into their process. Built into libassets/ next to the committed
# macOS .dylib (which can't be built on Linux) so go:embed carries every hook.
RUN apk add --no-cache zig
WORKDIR /src
COPY cli/ ./cli/
RUN cd cli/internal/inject && \
    zig cc -target x86_64-linux-gnu.2.28  -O2 -fPIC -shared -fvisibility=hidden \
        -o libassets/plug-hook-linux-amd64.so csrc/plug_hook.c -ldl && \
    zig cc -target aarch64-linux-gnu.2.28 -O2 -fPIC -shared -fvisibility=hidden \
        -o libassets/plug-hook-linux-arm64.so csrc/plug_hook.c -ldl
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

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
