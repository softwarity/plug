# In-cluster fetcher for the expose e2e: GET :8097/fetch?url=… does the GET
# from inside the cluster and returns the body. Build context is e2e/.
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY services/prober/go.mod ./
COPY services/prober/*.go ./
RUN CGO_ENABLED=0 go build -o /prober .

FROM alpine:3.21
COPY --from=build /prober /usr/local/bin/prober
EXPOSE 8097
ENTRYPOINT ["/usr/local/bin/prober"]
