# e2e — coverage matrix

Proves that a client app, launched **under plug**, reaches a cluster service **by
its name** — across a matrix of **languages × protocols**. This is the regression
guard: if a change breaks a covered cell, the suite goes red before you retest by
hand.

## Shape

- **One CI job per protocol** (`.github/workflows/e2e.yml`, matrix): each job
  stands up `agent` + **one** service, then runs **every language client** under
  plug against it. Isolated, parallel, and a red cell says exactly which
  protocol/language broke. Adding a protocol = one more job; no heavy cluster.
- **Clients** (`clients/<lang>/`) — Go, Node, Python, Java. Each is one small
  program: `client <proto> <host:port>`, using the language's **natural driver**
  for that protocol, doing the minimal op that proves the connection (`SELECT 1`,
  `PING`, publish/consume, health check…) and printing `E2E-OK <proto>`.
- **Services** — stock images (httpbin, postgres, redis, mongo, rabbitmq,
  mosquitto) + a tiny gRPC health server (`services/grpc/`). On `internal` only,
  so a client (on `edge`) can reach them **only** through plug.

## Protocols

`http` · `postgres` · `redis` · `mongo` · `amqp` · `mqtt` · `grpc` — a
representative of each connection *behaviour*: short req/resp, long binary
session, messaging, pub/sub, HTTP/2 multiplexing.

## Run locally

    bash e2e/run.sh                    # the whole matrix (every protocol)
    bash e2e/run.sh http postgres      # a subset of protocols
    E2E_LANGS="go java" bash e2e/matrix.sh redis   # one protocol, some clients

The first run builds the agent image (`docker rmi softwarity/plug:e2e` to force a
rebuild after changing plug itself). Inside a container the clients need
`seccomp:unconfined` + `SYS_PTRACE` (in `compose.yml`) for the Go-coverage
supervisor — a real host needs neither.

## Known gaps

Cells in `E2E_XFAIL` (default `java:grpc python:grpc`) warn instead of failing.
plug routes both **by name** correctly — these are two distinct, hard cases:

- **java:grpc** — plug splices the intercepted connection with `dup2()`, which
  invalidates the fd's epoll registration; Netty arms its event loop *before*
  `connect()` returns, so it never sees the connection come up (go/node/libc
  register after connect → fine).
- **python:grpc** — grpcio resolves via **c-ares**, which does DNS with
  `sendto()` to the real nameserver, bypassing both interception layers, so a
  cluster name never reaches plug's resolver.

See RELEASE_NOTES for the fix directions.
