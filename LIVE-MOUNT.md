# Live mount: the workload's volumes on your machine

The design and the working notes of the live mount, kept so that a problem
found later has its base: what was decided and why, how the pieces talk, what
was proven and how, where to look when something is off. The user-facing
description lives in the README, the CLI reference and the release notes;
this file is the one behind them.

## What it does

A takeover (`-s`) or `--env-of` mounts every data volume of the workload - a
Docker volume, a bind, a Kubernetes PersistentVolumeClaim - on the developer's
machine, live and read-write, for the length of the session, without being
told which. Each volume lands under the session's temp directory at its
cluster path (`/tmp/plug-vol-XXXX/opt/geoserver/data_dir`), and the variables
that name that path are repointed there, so the process finds its data where
its environment says and knows nothing. `--no-mount` turns it off,
`--no-mount=/a,/b` leaves those out; `--mount [<name>:]<volume-or-path>[:<local>]`
is the explicit form, at the exact path, for a process that hard-codes one.

## The architecture, and the decisions behind it

```
process ─ OS's own SMB client ─ 127.0.0.1:<port> (plug's forward) ─ tunnel ─ agent ─ helper (Samba, the volume at /mnt/vol)
```

- **The helper is the agent's image** (`plug-agent mount-serve`, `agent/mountserve.go`),
  started beside the workload with the volume mounted: a container on the
  agent's network (Docker), a service on the overlay pinned to the volume's
  node (Swarm), a pod pinned to the workload's node (Kubernetes). It runs
  Samba for one share behind one throwaway credential. Mounting the PVC is
  what the workload does - normal, not a defect. No second image: Samba is
  in the agent image (`agent/Dockerfile`, the one `apk add` of the final
  stage, retried), +80 MB, pulled once with the agent, nothing on demand and
  nothing more to mirror in a private registry.
- **The client installs nothing.** It reaches the helper through the tunnel
  it already has - a `direct-tcpip` channel, what plug does for every cluster
  service - behind a local forward (`mountForward` in `cli/mount.go`), and
  mounts it with the SMB client the OS ships with: `mount_smbfs` on macOS,
  run as the user (no privilege; the mount is the user's), the kernel's cifs
  module on Linux through `mount(2)` with plug's `cap_sys_admin` (no
  cifs-utils). **Windows** has no forward: its redirector speaks to port 445
  only and a UNC path carries no port, so the share is reached BY NAME
  (`\\<helper>\vol`) through plug's own DNS - a fake address into the
  datapath, the tunnel, the helper - and `net use` maps it to a DRIVE LETTER
  (a free one from Z down for an automatic mount, `--mount /data:Y:` to name
  one; a directory needs the symlink privilege and says so). The helper
  answers a NAME on every backend for this: the container's alias, the Swarm
  service, and on Kubernetes a Service plug creates in front of the pod.
- **`--dockerrun`** mounts nothing on the host: Docker Desktop hangs on
  binding a network mount into its VM (seen, on macOS). The container gets a
  docker VOLUME of type cifs instead, created by plug, that the daemon's own
  kernel mounts through the forward plug keeps open (`host.docker.internal`
  on Desktop, the host's loopback on Linux), at the cluster path, read-write.
  Removed with the container.
- **Why SMB, why not FUSE, why not NFS.** FUSE means a runtime to install on
  the workstation (fuse-t or macFUSE on macOS, WinFsp on Windows). NFS has
  no first-class client on Windows. SMB is native on all three, and Samba is
  *the* SMB server. The only SMB2 server written in Go is AGPL and has no
  authentication, so it is Samba in the image, as a separate process.
- **The credential is minted by the client**, not the agent, so a
  re-provision after a reconnect (a new liveness port, a new helper) keeps
  the OS's own SMB reconnection authenticating: the forward is retargeted at
  the new helper, the mount never moves.
- **Option B for the automatic mounts** (temp dir + repointed variables),
  the same shape the mounted secret files take, for the same reason: the
  exact path is not always creatable here (macOS seals its root, plug on
  Linux is not root). `--mount` gives the exact path when it is.

## The pieces

Agent (`agent/mount.go`, `agent/mountserve.go`):
- `volumes-of <name>` - the workload's data-volume paths (never a tmpfs,
  never `/run/secrets`, which files-of owns). Docker: the container's
  `Mounts` (through the same candidates env-of uses: parking receipt, running
  owner, the name). Swarm: the service spec's mounts. Kubernetes: the pods
  behind the Service (or the receipt's selector when parked), PVC-backed
  mounts only.
- `mount-volume <name> <volume-or-path> <agent-port> <password>` - starts the
  helper, answers `mounted host=<name> port=445 share=vol user=plug`. The
  helper's name carries (workload, volume, session port): one per session,
  so two developers may mount one volume, and a re-provision is a new helper
  beside the old one.
- `unmount-volume <name> <volume-or-path> <agent-port>` - removes it.
- `mount-status <name> <volume-or-path> <agent-port>` - where the helper
  stands, in the orchestrator's own words ("pending: no suitable node
  (scheduling constraints not satisfied on 2 nodes)", "ImagePullBackOff",
  "exited, exit code 1"): what the client's progress line shows while it
  waits, and the error when the helper never answers.
- The helper carries `plug.mount=1`, `plug.mount.of`, `plug.mount.volume`
  and the session owner (`plug.session.owner`, a label on Docker/Swarm, an
  annotation on Kubernetes since a label cannot hold `host:port`).
  `sweepMountHelpers` runs with every sweep (boot gc and every minute) and
  removes a helper whose session no longer answers.
- `mount-serve` (the helper's process): writes `smb.conf` (SMB2+, no NetBIOS,
  `fruit` for macOS), the account's NT hash straight into the smbpasswd file
  (no smbpasswd binary), serves as the volume owner's uid (`force user`) so
  what the developer writes is what the workload reads back, runs `smbd` as a
  child and relays the stop signal.
- RBAC (`deploy/plug-k8s.yaml`): `pods` create/delete, `persistentvolumeclaims`
  get (to refuse a ReadWriteOncePod claim with the reason).

Client (`cli/mount.go`, `cli/mount_{darwin,linux,windows}.go`):
- `startMounts(cfg)` runs BEFORE `startExposes` at the three launch sites, so
  the environment projection sees the mounts, and before the child exists (on
  Linux it clones its mount namespace from this one). It dials its own
  transport, opens a liveness forward (`Expose` of a spec whose local port
  refuses: nothing connects, the agent's sweep only asks whether it answers),
  asks `volumes-of` for the automatic names (`-s` names and `--env-of`), then
  for each mount: `mount-volume`, wait for the helper's port, start the local
  forward, mount, record. `autoMounts` is what the projection reads
  (`autoMountsFor`, one cluster path → local path per mount) to repoint
  variables with `localizeVolumeEnv`.
- Each mount is a progress line on stderr (`cli/progress.go`): redrawn in
  place with a spinner, the step and the elapsed time on a terminal, plain
  lines otherwise; the helper's state is asked every 3 s while waiting.
- `OnRearm` of the liveness forward re-provisions every helper under the new
  port and retargets the forwards (and re-pins the new name on Windows).
- Windows and the multicluster router: the kernel's SMB client is nobody's
  child, so the ancestry walk would refuse its flow. A session PINS the
  helper's name to its cluster (`tun.PinName`, a `.pins` sidecar beside its
  registry marker), and the router (`multiDial`) attributes a pinned name
  before any walk; the dialFunc carries the destination name for it.
- Teardown: unmount, close the forward, `unmount-volume`, forget the record,
  remove the session directory once nothing is mounted under it.
- Records (`~/.plug/mounts/`, `key = value` files like served names): pid,
  path, spec, local address. `sweepOrphanMounts` runs before any mount and
  unmounts what a dead session left; `plug doctor` reports it ("live
  mounts"), `--fix` unmounts.
- macOS: `mount_smbfs -N //user:pass@127.0.0.1:port/share path` as the user
  (applyPrivDrop). SMB multichannel is turned off for the loopback server
  through a scoped section plug adds once to the real user's
  `~/Library/Preferences/nsmb.conf` (`[127.0.0.1] mc_on=no`): after a
  reconnect, macOS looked for the NIC behind 127.0.0.1, found none
  (`smb2_mc_update_main_channel: could not find one of the nics`) and dropped
  the session it had just recovered. `umount`, then `diskutil unmount` as a
  fallback (diskarbitrationd has its own identity). After mounting, plug
  probes the mountpoint once and, on EPERM, names TCC (see below).
- Linux: `unix.Mount("//127.0.0.1/vol", path, "cifs", 0, "ip=…,port=…,user=…,pass=…,vers=3.0,uid=…,gid=…,file_mode=0664,dir_mode=0775,noperm,nobrl")`,
  lazy detach on a busy unmount.

The wire: `--mount` and `--no-mount` cross the launcher→core exec raw, like
`-s` and `--no-env`, and are stripped back by `stripLeadingAll`. An agent
older than the verbs answers "unknown command": the automatic mounts then
mount nothing (the session is as before), an explicit `--mount` says which
version to upgrade to.

## What was proven, and how to run it again

- `cli/mount_e2e_test.go`, against a real agent (`PLUG_MOUNT_E2E=host:port`,
  a cluster with a workload `geo` mounting a volume at `/data`;
  `PLUG_MOUNT_E2E_RW=1` to read and write through the mount where the shell
  may, `PLUG_MOUNT_E2E_ORPHAN=1` and `PLUG_MOUNT_E2E_AGENT_CONTAINER=<name>`
  for the two lifecycle tests):
  - mount, teardown, helper gone, record gone;
  - **kill -9** of a session holding a mount: the mount outlives the process,
    the next run's sweep unmounts it and forgets the record, the agent's
    sweep reaps the helper on its own within its minute;
  - **agent restart** under a held mount: the liveness forward re-arms, the
    helper is re-provisioned with the same credential, the mount is intact;
  - the automatic mounts: mounted under the session directory, named by
    `autoMountsFor`, unmounted and the directory removed at teardown;
    `--no-mount` mounts nothing.
- On Linux, in a privileged container against a local Compose cluster
  (Docker Desktop's kernel has cifs built in): the real binary,
  `plug -c --env-of vol-linux` with no flag - `$VOL_DIR` repointed, seed
  read, file written and served by the workload by name.
- In CI, the "Live mount" cell on the three families × three OSes: automatic
  (`-c --env-of vol-<os>`, `$VOL_DIR` repointed) and explicit (`--mount`),
  each reading the workload's seed through the mount and writing a file the
  workload then serves over http, fetched by name through plug; the path
  unmounted after. Windows asserts that the session runs without the
  automatic mount and that `--mount` is refused.

## Where to look when something is off

- `plug doctor`: "live mounts" says what a dead session left; `--fix`
  unmounts. The helper: `docker ps --filter label=plug.mount=1` /
  `docker service ls` / `kubectl get pods -l plug.mount=1`; its log is smbd's
  (`docker logs`, `kubectl logs`).
- "Operation not permitted" on every file of a mount that succeeded, on
  macOS: TCC. Since Catalina a process only touches a NETWORK VOLUME if the
  app it runs under has the permission (System Settings > Privacy & Security
  > Files and Folders > Network Volumes). Terminal asks once; a shell inside
  an editor may never have asked. plug says it after mounting. The server is
  not the problem: `smbclient` reads and writes through it.
- The mount dies a few seconds after a reconnect, on macOS: the nsmb.conf
  section is missing (`~/Library/Preferences/nsmb.conf`, `[127.0.0.1]`,
  `mc_on=no`); the kernel log says `smb2_mc_update_main_channel`.
- The helper never answers on 445: its log. A Swarm helper Pending: the
  volume is on another node than the one it was placed on (best-effort from
  the last task's node). A Kubernetes helper Pending: the claim is
  ReadWriteOncePod (refused with the reason when the RBAC lets plug read the
  claim), or the RBAC predates `pods` create.
- "no container answers to X": the workload is not reachable under that name
  from the agent's networks (the same rule as env-of).

## Watch list

Not defects in what ships; the points to weigh if the feature becomes a
sensitive one.

- Windows: an explicit `--mount` at a DIRECTORY needs the symlink privilege
  (Developer Mode or elevation); a drive letter needs nothing. The redirector's
  own reconnection after a tunnel blip is Windows' to do (not exercised).
- SQLite/GeoPackage files over SMB: range locks are weaker than local ones;
  flat files are fine.
- Swarm on several nodes: the helper follows the node of the workload's last
  task (what Swarm itself would give the workload); a service that never ran
  has no node to follow. A helper that cannot be placed says so through
  mount-status ("no suitable node …") rather than timing out mute.
- The agent image is +80 MB for Samba.
- Throughput and per-operation latency are those of SMB over an SSH tunnel:
  fine for a development session, not a data pipeline.
