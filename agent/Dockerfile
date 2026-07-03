# Build context is the REPO ROOT (so the CLI sources are available):
#   docker build -f agent/Dockerfile -t softwarity/plug-agent .
# VERSION is stamped into every binary and written to /opt/plug/VERSION;
# the CLI compares against it to decide whether to self-upgrade.
ARG VERSION=dev

FROM golang:1.24-alpine AS build
ARG VERSION
WORKDIR /src
COPY cli/ ./cli/
WORKDIR /src/cli
RUN mkdir -p /out && \
    for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
      os=${t%/*}; arch=${t#*/}; ext=""; [ "$os" = windows ] && ext=".exe"; \
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o "/out/plug-$os-$arch$ext" .; \
    done

FROM alpine:3.21
ARG VERSION

# openssh: transport for sshuttle | python3: sshuttle server side | iproute2: subnet discovery
RUN apk add --no-cache openssh-server python3 iproute2 \
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

COPY --from=build /out/ /opt/plug/bin/
RUN printf '%s' "${VERSION}" > /opt/plug/VERSION

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
