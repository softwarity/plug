FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY web/ web/
RUN CGO_ENABLED=0 go build -o /web ./web

FROM debian:bookworm-slim
COPY --from=build /web /usr/local/bin/web
CMD ["/usr/local/bin/web"]
