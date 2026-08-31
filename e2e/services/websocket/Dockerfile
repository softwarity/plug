# Tiny WebSocket echo server. Build context is e2e/.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY services/websocket/go.mod services/websocket/go.sum ./
RUN go mod download
COPY services/websocket/*.go ./
RUN CGO_ENABLED=0 go build -o /wssrv .

FROM alpine:3.24
COPY --from=build /wssrv /usr/local/bin/wssrv
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/wssrv"]
