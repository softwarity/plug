# The "developer laptop": the real plug binary (pulled from the agent image, so
# it carries the embedded hook), plus curl (libc) and goraw (Go) test clients.
ARG AGENT_IMAGE=softwarity/plug:e2e
FROM ${AGENT_IMAGE} AS agentimg

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY goraw/ goraw/
RUN CGO_ENABLED=0 go build -o /goraw ./goraw

FROM debian:bookworm-slim
ARG TARGETARCH
# openssh-client: plug's launcher shells out to `ssh` for its version check /
# core download (the data path itself is in-process Go SSH).
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates openssh-client \
    && rm -rf /var/lib/apt/lists/*
COPY --from=agentimg /opt/plug/bin/plug-linux-${TARGETARCH} /usr/local/bin/plug
COPY --from=build /goraw /usr/local/bin/goraw
COPY run-cases.sh /usr/local/bin/run-cases.sh
RUN chmod +x /usr/local/bin/plug /usr/local/bin/goraw /usr/local/bin/run-cases.sh
CMD ["/usr/local/bin/run-cases.sh"]
