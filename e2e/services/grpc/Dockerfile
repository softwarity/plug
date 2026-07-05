# Tiny gRPC health server. Build context is e2e/.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY services/grpc/go.mod services/grpc/go.sum ./
RUN go mod download
COPY services/grpc/*.go ./
RUN CGO_ENABLED=0 go build -o /grpcsrv .

FROM alpine:3.21
COPY --from=build /grpcsrv /usr/local/bin/grpcsrv
EXPOSE 50051
ENTRYPOINT ["/usr/local/bin/grpcsrv"]
