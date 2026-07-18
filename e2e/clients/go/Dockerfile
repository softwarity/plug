# The Go "developer laptop": plug (from the agent image, so it carries the
# embedded hook + seccomp supervisor) + the Go polyglot client. Build context is
# e2e/. Run under plug via the entrypoint; args are `<proto> <host:port>`.
ARG AGENT_IMAGE=softwarity/plug:e2e
FROM ${AGENT_IMAGE} AS agentimg

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY clients/go/go.mod clients/go/go.sum ./
RUN go mod download
COPY clients/go/*.go ./
RUN CGO_ENABLED=0 go build -o /eclient .

FROM debian:bookworm-slim
ARG TARGETARCH
# openssh-client: plug's launcher shells out to ssh for its version check.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssh-client iptables iproute2 \
    && rm -rf /var/lib/apt/lists/*
COPY --from=agentimg /opt/plug/bin/plug-linux-${TARGETARCH} /usr/local/bin/plug
COPY --from=build /eclient /usr/local/bin/eclient
RUN chmod +x /usr/local/bin/plug /usr/local/bin/eclient
# PLUG_ROOT=1 → run under the kernel-redirect root mode (needs cap NET_ADMIN).
ENTRYPOINT ["plug", "-H", "agent", "--port", "22", "-s", "e2e-go:18074:9", "/usr/local/bin/eclient"]
