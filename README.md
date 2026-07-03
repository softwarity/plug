# plug

Run a local process **as if it were inside your Docker Swarm cluster**: cluster
DNS names resolve, cluster services are reachable — no code change, no proxy
config in your app.

```bash
plug npm run start:dev
# your local NestJS/Spring/Quarkus now resolves and reaches
# http://my-service:8080 like any container in the stack
```

## How it works

```
┌─ your laptop ──────────────┐         ┌─ swarm cluster ───────────────┐
│  plug <cmd>                │         │  plug-agent (alpine + sshd)   │
│   ├─ discovers subnets ────┼──ssh────┼─→ attached to overlay nets    │
│   ├─ sshuttle tunnel ──────┼──:2222──┼─→ relays traffic + DNS        │
│   └─ runs <cmd>            │         │   (resolver 127.0.0.11)       │
└────────────────────────────┘         └───────────────────────────────┘
```

`plug` starts an [sshuttle](https://github.com/sshuttle/sshuttle) tunnel to a
tiny agent container deployed in the cluster, auto-discovers the overlay
subnets from the agent itself, routes them (plus DNS) through the tunnel, runs
your command, and tears everything down when it exits.

## Setup

**Once, on the cluster:**

```bash
plug init                                  # writes plug-stack.yml
# edit the networks: section to list your overlay networks
docker stack deploy -c plug-stack.yml plug
```

**On each dev machine:**

```bash
brew install sshuttle
make cli && make install                   # or grab a release binary
```

## Usage

```bash
plug --host swarm-node.example.com npm run start:dev
```

Or save profiles in `~/.plug/`:

```ini
# ~/.plug/staging.conf
host = swarm-node.example.com
port = 2222
# subnets = 10.0.9.0/24        # optional, skips auto-discovery
```

```bash
plug -p staging ./mvnw spring-boot:run
plug -p staging npm run start:dev
```

`$PLUG_HOST` / `$PLUG_PORT` work too. sudo will prompt once per session
(sshuttle needs it for local packet redirection).

## Security model — read this

**There is deliberately no authentication.** The SSH keypair is embedded in
this repository and in every `plug` binary; it is a transport detail, not a
secret. Anyone who can reach the agent port has full network access to the
attached overlay networks.

Only deploy the agent on clusters and networks you already trust (internal dev
clusters). Never publish port 2222 on an untrusted network.

## Roadmap

- [ ] Kubernetes transport (agent pod + `kubectl exec`)
- [ ] Embed the agent into an API gateway (dynamic enable/disable)
- [ ] Homebrew tap
