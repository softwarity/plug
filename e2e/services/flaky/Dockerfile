# Outage simulator: :8099 answers only after GET :8098/up. Build context is e2e/.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY services/flaky/go.mod ./
COPY services/flaky/*.go ./
RUN CGO_ENABLED=0 go build -o /flaky .

FROM alpine:3.24
COPY --from=build /flaky /usr/local/bin/flaky
EXPOSE 8098 8099
ENTRYPOINT ["/usr/local/bin/flaky"]
