# Agent-crash simulator: GET :8095/restart-agent restarts the agent container
# via the mounted docker.sock. Build context is e2e/.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY services/chaos/go.mod ./
COPY services/chaos/*.go ./
RUN CGO_ENABLED=0 go build -o /chaos .

FROM alpine:3.24
COPY --from=build /chaos /usr/local/bin/chaos
EXPOSE 8095
ENTRYPOINT ["/usr/local/bin/chaos"]
