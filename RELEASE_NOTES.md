# Release Notes

## NEXT RELEASE

### Features

- `plug <command>`: run a local process with cluster DNS and subnets, tunnelled
  through a tiny agent container (sshuttle over ssh, no auth by design)
- Auto-discovery of the overlay subnets from the agent itself
- Profiles in `~/.plug/*.conf`, `--host`/`--port` flags, `$PLUG_HOST`/`$PLUG_PORT`
- `plug init` writes an example swarm stack
- Multi-arch agent image (linux/amd64, linux/arm64)
