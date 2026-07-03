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

**Once, on the cluster** — the descriptor lives in this repo,
[deploy/plug-stack.yml](deploy/plug-stack.yml):

```bash
curl -fsSLO https://raw.githubusercontent.com/softwarity/plug/main/deploy/plug-stack.yml
# edit the networks: section to list your overlay networks
docker stack deploy -c plug-stack.yml plug
```

**On each dev machine:**

```bash
brew install sshuttle          # macOS; linux: apt/dnf install sshuttle
```

Then grab the `plug` binary for your platform from the
[releases page](https://github.com/softwarity/plug/releases), or build it
yourself with `make cli && make install`.

> **Windows**: a `windows-amd64` binary is published, but sshuttle has no
> native Windows support — run plug inside WSL2 (with the linux binary)
> instead.

## Usage

```bash
plug npm run start:dev
plug ./mvnw spring-boot:run
```

Profiles live in `~/.plug/*.conf` and are picked automatically:

- **no profile** → a short wizard asks for a name, the cluster host and the
  agent port (default 2222), then saves and uses it
- **one profile** → used as is
- **several profiles** → interactive selection, or pick one with `-p staging`

`plug init` runs the same wizard on demand (e.g. to add a second cluster).
A profile is a plain file you can also edit by hand:

```ini
# ~/.plug/staging.conf
host = swarm-node.example.com
port = 2222
# subnets = 10.0.9.0/24        # optional, skips auto-discovery
```

`--host`/`--port` (or `$PLUG_HOST`/`$PLUG_PORT`) bypass profiles entirely.
sudo will prompt once per session (sshuttle needs it for local packet
redirection).

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
