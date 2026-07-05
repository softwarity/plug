# The Node.js "developer laptop": plug (from the agent image, so it carries the
# embedded hook + seccomp supervisor) + the Node polyglot client. Build context
# is e2e/. Run under plug via the entrypoint; args are `<proto> <host:port>`.
ARG AGENT_IMAGE=softwarity/plug:e2e
FROM ${AGENT_IMAGE} AS agentimg

FROM node:20-bookworm-slim
ARG TARGETARCH
# openssh-client: plug's launcher shells out to ssh for its version check.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssh-client \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY clients/node/package.json ./
RUN npm install --omit=dev
COPY clients/node/client.js clients/node/health.proto ./
COPY --from=agentimg /opt/plug/bin/plug-linux-${TARGETARCH} /usr/local/bin/plug
RUN chmod +x /usr/local/bin/plug
ENTRYPOINT ["plug", "-H", "agent", "--port", "22", "node", "/app/client.js"]
