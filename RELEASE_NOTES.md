# Release Notes

## NEXT RELEASE

---

## 0.1.0

### Features

- `plug <command>`: run a local process with cluster DNS and subnets, tunnelled
  through a tiny agent container (sshuttle over ssh, no auth by design)
- Auto-discovery of the overlay subnets from the agent itself
- Profiles in `~/.plug/*.conf` with automatic selection (one → used, several →
  interactive choice, none → creation wizard); `plug init` runs the wizard
- `--host`/`--port` flags and `$PLUG_HOST`/`$PLUG_PORT` bypass profiles
- Multi-arch agent image (linux/amd64, linux/arm64)
