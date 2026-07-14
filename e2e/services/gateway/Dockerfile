# API-gateway simulator for the reverse-direction e2e: POST /call {service,port,id}
# → in-cluster GET http://service:port?id=id → returns the answer. Build context is e2e/.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY services/gateway/go.mod ./
COPY services/gateway/*.go ./
RUN CGO_ENABLED=0 go build -o /gateway .

FROM alpine:3.24
COPY --from=build /gateway /usr/local/bin/gateway
EXPOSE 8100
ENTRYPOINT ["/usr/local/bin/gateway"]
