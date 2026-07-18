# The Python "developer laptop": plug (from the agent image, so it carries the
# embedded hook + seccomp supervisor) + the Python polyglot client. Build context
# is e2e/. Run under plug via the entrypoint; args are `<proto> <host:port>`.
ARG AGENT_IMAGE=softwarity/plug:e2e
FROM ${AGENT_IMAGE} AS agentimg

FROM python:3.12-slim-bookworm
ARG TARGETARCH
# openssh-client: plug's launcher shells out to ssh for its version check.
# iproute2: the TUN root mode configures the device + routes with `ip`.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates openssh-client iproute2 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY clients/python/requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY clients/python/client.py ./
COPY --from=agentimg /opt/plug/bin/plug-linux-${TARGETARCH} /usr/local/bin/plug
RUN chmod +x /usr/local/bin/plug
# PLUG_ROOT=1 → run under the TUN root mode (needs cap NET_ADMIN + /dev/net/tun).
ENTRYPOINT ["plug", "-H", "agent", "--port", "22", "-s", "e2e-python:18076:9", "python", "/app/client.py"]
