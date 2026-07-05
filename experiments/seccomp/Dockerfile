# Linux-only proof (seccomp) — built & run in a container since it can't run on macOS.
FROM golang:1.26-bookworm
WORKDIR /app
COPY go.mod ./
COPY supervisor.c run.sh ./
COPY target/ target/
COPY backend/ backend/
RUN gcc -O2 -Wall -o supervisor supervisor.c
RUN go build -o gotarget ./target && go build -o backendsrv ./backend
RUN chmod +x run.sh
CMD ["./run.sh"]
