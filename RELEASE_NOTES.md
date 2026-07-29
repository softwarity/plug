# Release Notes

## NEXT RELEASE

### Changed: `plug update` looks the registry up from your machine

The lookup behind `update` — list the tags, pick the target — ran on the agent,
whose traffic leaves the cluster through the Docker Desktop VM and follows the
workstation's DNS: plugged during a session, ~31s per registry round-trip. The
CLI now asks the agent which image it carries (`info` names it), resolves the
target against that image's own registry from your machine (~1s), and hands the
agent an already checked tag to apply (`self-update apply <tag>`). An
unpublished tag is refused before anything is asked of the cluster.

The agent-side lookup remains the fallback, tried after a 4-second budget: a
registry only the cluster can reach, a moving tag (whose currentness is a
digest question only the cluster can answer), an agent from before this
existed — or an outbound firewall (LuLu et al.) blocking plug's first registry
call, which is also worth allowing once.

Concurrent updates need no lock: the orchestrator already serializes them —
Swarm's service update is a compare-and-swap on the spec's version, Kubernetes
converges on the last patch, and the launcher self-replace is an atomic rename.
The loser of the race now gets a plain "another update reached the cluster
first" instead of the rpc noise.

Also fixed on the way: an image pinned by digest alone (`repo@sha256:…`, no
tag) was read as `latest` once the digest was stripped — an update would have
quietly switched the deployment onto the moving stream. It now follows the
release channel, which is what a pin means.


---

## 2.5.2

### Fixed: a stale NXDOMAIN no longer outlives a name's re-provisioning

plug's negative DNS answers carried no SOA, so the OS picked its own negative
cache duration — and macOS held one long enough that a `-s` name swept during an
agent restart kept failing instantly on the whole machine after it was back,
without the lookup ever reaching plug again. Negative answers now carry a SOA
bounding that cache to 5 seconds: absent stays absent, but never longer than it
really was. (Immediate remedy on an affected machine:
`sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder`.)

---

## 2.5.1

### Fixed: on Swarm, creating a `-s` name no longer waits on the registry

The signpost service was created with a bare tag, so the Swarm manager resolved
it against the registry at create time — and on a plugged workstation that
lookup rides the session's own DNS detour: three ~31s registry round-trips per
signpost, which was the whole `-s` wait. The agent now pins the image to the
digest the local engine already knows (the signpost runs the agent's own image),
leaving the manager nothing to resolve: 0.6s to Running, measured with a session
active. Bare tags are what `plug update` writes (it drops the digest to move the
deployment), so every updated agent used to reintroduce the wait.

### Added: the update path is now covered end to end

`plug update` was asserted only as "the verb answers and the agent survives" —
the agent under it runs an unpublished tag, so it could never actually move.
Three cells now run against agents deployed from a **published release**, on all
three OSes:

- a 2.4.1 agent (the oldest that can retarget itself) must ask the registry and
  name a newer release — the decision `plug update` exists to make;
- `update <tag>` must refuse a tag the registry does not have, and leave the
  agent standing;
- `update <tag>` against that 2.4.1 agent must refuse on its version, rather
  than degrade into a plain self-update that silently ignores the target.

---

## 2.5.0

> **BREAKING — an agent without orchestrator access no longer starts.** No Docker
> socket mounted (or, on Kubernetes, no RBAC): the container now exits 1 instead
> of coming up. A cluster running plug only for the outbound tunnel never needed
> that access — **add the mount before updating**, or that agent will not come
> back up. The container prints the stack-file lines that fix it.

### Fixed: `-s` starts your command immediately

Exposing a name used to cost 11.5s before your command ran, on a healthy
cluster. All of it was one wait: the agent creates the signpost and answers at
once, but the cluster still has to schedule it — measured at 6s, 37s and 29s on
three identical runs of the same Swarm. plug blocked on that, and a probe sent
too early cannot fail fast (a VIP with no task behind it drops the SYN), so the
check burned a full 10s timeout to learn nothing.

The path is now proven in the **background**. Hand-back went from 11.5s to
**0.06s**; traffic reaches your process the moment the cluster is ready. Short
probes (0.75s → 8s) catch that moment instead of waiting it out, and the overall
budget went from 44.5s to 90s — the old one sat in the middle of the spread a
real cluster produces (3s to 62s observed), which is exactly when a wait turns
into an intermittent `not reachable inside the cluster`.

Everything decidable at once still fails the session in milliseconds: a port
already exposed, a name refused, a workload that cannot be parked. A name that
never comes up becomes a `WARNING` during the session, naming the command that
shows why (`docker service ps plug-sp-<name>`), instead of a refusal to start.

### Added: `plug update <tag>` switches the channel a cluster follows

```bash
plug -p neo update tag        # the newest release published
plug -p neo update latest     # the latest stream
plug -p neo update feat-09    # a branch's tag, at whatever it points to now
plug -p neo update 2.3.0      # an exact release — downgrades included
```

Release and stream are told apart by the tag itself: `x`, `x.y` or `x.y.z` is a
release, anything else moves under you. The tag is checked against the registry
**before** anything is repointed — aiming a deployment at a tag nobody published
leaves an agent that cannot pull. Without a target the command is unchanged.

### Changed: orchestrator access is now required, not optional

`-s` used to degrade to a *static* mode when the agent could not create names,
leaving you to pre-declare them yourself. That mode is gone, along with the
compatibility shims for agents older than this release. Provisioning is the
feature; an agent that cannot do it is a deployment mistake, which is why it now
refuses to start (above) and `plug doctor` reports it as a failure.

Unaffected: the outbound tunnel needs no socket and no RBAC, and never did.

### Fixed: an agent's commit hash still showed at launch

2.4.1 stopped stamping releases with their commit, but only for images built
from then on: an agent still on 2.4.0 or earlier reports `2.4.0+983761c`, and
every line that printed it kept the suffix — `using cluster version` at launch
included. Versions are now rendered short wherever they are shown for reading. A
branch build keeps its revision, which is the only thing telling two apart, and
the cached-core list keeps it too, since those are directory names.

### Fixed: a CI cell was passing without testing anything

`compat-launcher.sh` decides what an old launcher can be asked to do by walking
git history from the revision in its version string — which only exists in a
`dev+<rev>` build. Releases stopped carrying that suffix in 2.4.1, so both its
guards read a bare `2.4.1`, found no revision, and answered "too old" for a
launcher newer than every fix they guarded: the Linux leg exited green having
tested nothing, and mac/Windows ran without the `-s` a session now requires.
The guards are gone rather than repaired.

The `e2e` job is now `e2e-compose`, next to `e2e-k8s` and `e2e-swarm`. Update any
branch protection rule that requires the old check name.

---

## 2.4.1

### Fixed: `plug update` now actually moves a pinned cluster forward

An agent deployed from a pinned release tag could never be updated. `plug update`
asked the agent to refresh itself, the agent re-resolved its own tag —
`softwarity/plug:2.3.0` resolves to 2.3.0, today and forever — and the command
finished 90 seconds later reporting that nothing had changed. The one situation
where you reach for `update` was the one where it did nothing.

It now **rewrites the tag**. The agent lists the releases published for its own
repository, picks the newest `x.y.z`, and moves the deployment to it — majors
included. plug is the infrastructure carrying your sessions, not an application
dependency held back for reproducibility: there is no version of "up to date"
that leaves a cluster on an old agent.

Each backend applies it its own way:

- **Swarm** — the service's image is updated to the new tag (the pinned digest
  dropped), and the task rolls.
- **Kubernetes** — the Deployment's container image is patched, alongside the
  restart annotation.
- **Compose / plain container** — the new image is pulled, and since a container
  cannot recreate itself, the reply carries the exact command that does, plus a
  reminder to change the tag in the compose file (otherwise the next `up` puts
  the old pin straight back).

A **moving** tag (`latest`, `main`, a branch) is left alone and merely re-pulled:
it already resolves to whatever its publisher last pushed, and repointing it
would override a deliberate choice.

Two side effects worth having. A pinned deployment that is already on the newest
release is now answered **immediately** — no workload rolled, no 90-second poll
for a change that cannot come. And the lookup goes to the registry that actually
holds the image, so a mirror or a private registry is asked about its own tags;
one that cannot be listed degrades to the previous behaviour and says why,
rather than blocking the update.

### Changed: released versions no longer carry a commit hash

`plug version` answered `2.3.0+bb03611`. The commit exists to tell two builds of
a **moving** tag apart — without it, two rebuilds of `main` look identical and
the CLI keeps serving its cached core. A release tag already designates exactly
one commit, so there the suffix only made every version harder to read. Releases
are now stamped bare (`2.4.0`); builds off a branch keep `dev+<rev>`.

Consequence, and it is the right one: rebuilding a release tag in place no
longer propagates to clients. A release is immutable — cut a new one.

---

## 2.4.0

### Added: name the local port and plug picks a free one

The third field of a `-s` may now be a **name** instead of a number:

```bash
plug -s web:8080:PORT  npm run dev -- --port={PORT}
```

plug allocates a free local port for the session, substitutes `{PORT}`
everywhere in your command, and arms the mapping on that same number. The
command line is the only channel — nothing is put in your process's
environment: one number, one way to hand it over, and no variable of yours
quietly overwritten.

Why: the **cluster** port is agreed in advance (it is what other workloads
dial), but the **local** one is nobody's business but yours. Pinning it is what
makes two projects fight over `3000`, what stops the same app running on two
branches at once, and what turns a shared CI runner into a race. Naming it
removes the negotiation entirely.

The two spellings are deliberate: bare on the left because the third field of a
`-s` can only ever be a port — nothing to disambiguate; braced on the right
because argv is free text, and a bare `PORT` would also rewrite
`--transport=PORTAL`. The halves must match, and a mismatch fails at startup
rather than silently — a `{TOKEN}` nothing declared would reach the child as a
literal and make it fall back to a port the cluster isn't forwarding to, and a
name nothing references would allocate a port the child is never told about.
Commands using braces for their own purposes (`awk '{print}'`) are untouched
when no `-s` names a port.

Pinned ports keep working, unchanged. Naming needs ≥ 2.4 on **both sides**: the
launcher checks the mapping before connecting, and the mapping then crosses the
launcher→core exec raw, so it is the cluster's own core that resolves it. An
older agent says which it is and points at the pinned form; `plug update` aligns
the two.

### Changed: license — AGPL-3.0 → FSL-1.1-Apache-2.0

plug is now licensed under the [Functional Source
License](https://fsl.software/) (FSL-1.1-Apache-2.0) instead of AGPL-3.0.
AGPL already let a competitor build a rival product with plug's code — the
one condition was sharing their own source back. FSL closes that: it's free
for **any purpose, including building it into your own product or using it
internally at a company** — the one thing it doesn't permit is a **competing
use** (offering plug itself, or a substitute for it such as a rival
hosted-gateway offering, to others). It converts to Apache-2.0 two years
after each release, the same terms already used by
[Meerkat](https://softwarity.github.io/meerkat/), the gateway on plug's
roadmap. Nothing else about how you use plug day to day changes.

---

## 2.3.1

### Improved: bare `plug version` says when the launcher lags your clusters

`plug version` answers for the launcher — not what sessions run (each cluster
runs its exact core). With clusters freshly updated, that bare answer read
like plug was old. When the local cache proves a cluster already served a
newer release, a one-line note now follows on stderr (tty only — the stdout
value stays bare for scripts): `a cluster already served v2.4.0 — this
launcher is v2.3.0; plug update aligns it`. No network involved: `version`
stays instant and offline.

### Fixed: Ctrl-C no longer leaves the terminal in raw mode

A terminal Ctrl-C is delivered by the kernel to the whole foreground process
group — your command included. plug additionally re-sent it, so the child saw
a **double SIGINT**; dev servers (webpack / `ng serve`, `nest --watch`…) treat
the second one as "force quit NOW" and died without restoring the terminal
they had put in raw mode — the shell then echoed `^[[A` on arrow-up instead
of walking the history. plug now catches SIGINT only to survive long enough
for its own teardown and relays nothing (the child already has it); a
**targeted** SIGTERM at plug alone — which the kernel does not group-deliver —
is still relayed. Your command's graceful-shutdown path (and your terminal)
now behave exactly as without plug.

---

## 2.3.0

### New: `plug doctor` — health-check everything plug touches

One read-only command that checks the whole chain and names the remedy next to
every finding: the binaries (launcher, cached cores — and the version the
privileged service/daemon ACTUALLY runs, the one thing the per-cluster version
mechanism does not refresh), the system state (a resolver still pointed at
plug with **no** live session — the stale state that once broke machine-wide
DNS), and each profile's cluster: agent reachable and its version, whether
`-s` will be **dynamic** there (docker sock / swarm / kubernetes RBAC — via a
new agent verb, `info`) and whether the agent predates 2.2 (no honest
NXDOMAIN, no `-c`). With sessions running it even probes the live datapath: an
absent name must answer NXDOMAIN — a minted fake means the running daemon
predates 2.2.

`--fix` applies the SAFE repairs on the way (today: purging a truncated
cached core — it re-downloads on next use). Anything touching privileges, your
own sessions or the cluster stays a printed remedy on purpose: a doctor that
silently escalates would lose the trust it is meant to build.

When problems are found on an interactive terminal, doctor offers to open a
**pre-filled GitHub issue** — the browser is both the login and the review
step, and the report is redacted first (hostnames and IPs masked, profiles
anonymized: the repo is public, your topology is yours). Paste-friendly for
support either way.

### New: `plug update` — one command updates the agent, then the CLI

plug's distribution point IS the agent image (the CLI installs *from* the
agent), so `plug update [-p profile]` walks that chain upstream, in order:

1. **The agent refreshes itself** from its registry — a new agent verb,
   `self-update`, and each backend does it its own way. **Kubernetes**: a
   rolling restart of its own Deployment (the node re-pulls the tag per
   `imagePullPolicy` — `Always` in the official manifest). **Swarm**: a forced
   service update with the pinned digest dropped, so the manager re-resolves
   the deployed tag. **Plain container** (Compose): it pulls the tag, and — a
   container cannot recreate itself — hands back the one command that does
   (`docker compose up -d plug`), image already local. WHICH version arrives
   stays where it belongs: in the deployed tag (`latest` follows releases; a
   pinned `2.2.0` is respected and said out loud).
2. **The launcher refreshes itself from the agent** when the agent is now a
   newer release, and re-applies the privileged grant (one sudo on
   macOS/Linux). On Windows nothing extra is needed: the datapath service
   starts on demand from the same `plug.exe`, so the next session simply runs
   the new binary — the service-vs-launcher version gap `doctor` warns about,
   closed. Never downward, never on a dev build.

Live `-s` sessions ride the agent roll out by design: the keepalive detects
the drop, the reconnect re-arms every forward on the new agent (the same
self-heal chain the resilience cell proves on every push).

The official Kubernetes manifest grants the agent three more verbs for this —
`deployments get/list/patch`, still namespace-scoped, still minimal. On a
cluster running the previous RBAC, `plug update` answers with the exact
remedy instead of failing opaquely.

---

## 2.2.0

### New: `-c` / `--client` — run a pure consumer of the cluster

Some processes will never be called back by the cluster: a GUI database tool
(DBeaver, MongoDB Compass), a one-off script, a batch consumer. Naming those
with `-s` was noise with a real cost — a port bound on the shared agent, a
signpost/Service created for a name nobody will ever call. Declare them a
**pure client** instead:

    plug -c "/Applications/MongoDB Compass.app/Contents/MacOS/MongoDB Compass"

`-c` reaches cluster services by name like any plugged process, but names
nothing and reserves nothing on the agent. It is **mutually exclusive** with
`-s` — a process either serves a name or is a pure client — and omitting both
still errors, now with the two shapes explained side by side: the 2.0 rule ("a
process in a cluster is a service, and a service has a name") stays the
default; `-c` is the explicit declaration of the exception. Needs an agent
≥ 2.2 (an older one is refused with the upgrade hint). On macOS, launch the app
binary directly (not `open -a` — that hands the process to launchd, breaking
the ancestry multicluster attribution relies on).

### Fixed: a name absent from the cluster now answers NXDOMAIN

plug used to mint a stand-in IP for ANY bare name and let the connect sort it
out — an absent name resolved "fine" and then hung or refused, which bit
hardest with a cluster running on the plugged workstation itself (Docker
Desktop forwards its containers' unknown lookups to the machine's DNS — plug —
and a name that existed nowhere came back with a phantom `198.18.x.x`). The
resolver now asks the agent whether the bare name exists in a connected
cluster before minting (new agent verb `resolve`, verdicts cached: found
5 min, absent 30 s so a service being deployed appears quickly) and answers an
honest **NXDOMAIN** otherwise — apps get *unknown host* immediately instead of
timeouts and *connection refused*. The agent discards answers inside
`198.18.0.0/15` (plug's own fake range: such an answer can only be an echo of
a plug resolver upstream, never a real service), which makes the check immune
to the very loop it fixes. Against an older agent the CLI mints as before.
Asserted end to end in CI on all nine legs (the "dns honesty" cell).

### Fixed: UDP to a named service is now dropped loudly

plug tunnels TCP only (the SSH channel is stream-only) — a named UDP flow was
silently discarded, leaving the app hanging with no diagnostic at all. The
session log now names it once per target:
`udp <name>:<port> dropped — plug tunnels TCP only (repeats hidden 30s)`.
DNS keeps being served in-stack as before; this is about every other UDP flow.

---

## 2.1.0

### Fixed: macOS — the DNS watchdog no longer restarts the resolver on every configd event

The daemon re-asserts its system-DNS override whenever macOS recomposes the
network config — but it also flushed the cache and HUP'd `mDNSResponder` each
time. On a machine where something keeps configd busy (observed live: a
`locationd` Wi-Fi-scan loop re-publishing the DHCP lease ~2/min), that restarted
the system resolver all day and intermittently failed **unrelated** lookups
machine-wide (`Could not resolve host: github.com`…). The watchdog now rewrites
the overridden keys **quietly** when the effective config (Global/Setup and the
resolver files) still points at plug, and coalesces real flushes to at most one
per 30 s. `daemon.log` lines are now timestamped.

### New: takeover — develop a service that is already deployed

The name of a `-s` mapping often belongs to the very service you are developing,
already deployed in the stack — until now plug refused it and asked you to remove
the service by hand. Now plug **parks** the deployed workload for the session and
**restores it when the session ends**: `plug -s service1…` already states the
intent (the same behaviour as Telepresence's intercept and mirrord's steal).

- **Docker / Compose** — the containers answering the name are stopped, and
  restarted afterwards.
- **Swarm** — the service is scaled to 0, and scaled back **to its original
  replica count**. A stack's `<stack>_<svc>` service is recognized by its short
  alias; a foreign alias (a service whose own name is unrelated) is refused.
- **Kubernetes** — the existing Service is repointed at the agent (selector +
  ports), the originals saved **in an annotation on the Service itself**, and
  re-patched back afterwards. The bundled RBAC role gains `update`/`patch` on
  Services (still namespace-scoped, Services only).

The parking receipt rides the signpost's labels (or the k8s annotation), so the
restore survives anything: session end, `unserve-name`, **agent crash** (the
boot gc restores parked workloads before sweeping orphaned signposts), and a
transport reconnect **re-parks** (the same self-heal that re-provisions the
name). The signpost is created *before* the workload is parked, so the name
keeps resolving throughout — a no-record gap would leak lookups to the upstream
resolver (bench-proven on the embedded DNS).

A name held by **another live plug session** is still refused — takeover
applies to deployed workloads only, never to another dev's session. Against an
older agent (2.0.x) the CLI falls back to that agent's own behaviour (a taken
name is refused, with an upgrade hint). Switch-time caveats, one per platform:
on Swarm, callers holding a cached DNS answer (JVM ~30 s) may see brief
connection errors (the address behind the name changes). On Kubernetes the
Service keeps its **ClusterIP** through park and restore, so cached DNS stays
valid and *new* connections reach your session immediately — but the pods keep
running (only the name is rerouted): a parked k8s workload still consumes
queues, and a caller holding a pre-switch **keep-alive connection** keeps
reaching the old pod until it closes or idles out. Proven in CI on **all three
backends** (park → traffic lands locally → restore, on all three OSes) — on
Swarm including the scale-back to the *original* count (the CI target runs 2
replicas); agent-crash recovery bench-proven.

### CI: the whole e2e chain now runs against Kubernetes and Swarm too

Every push replays the full mesh e2e on **three cluster families**: the compose
cluster, a **kind** cluster (upstream Kubernetes), and a **single-node Swarm** —
same services, same names on each. On Kubernetes the agent is applied from the
**published** `deploy/plug-k8s.yaml` — RBAC included, only the image swapped —
so each push blesses the exact manifest users deploy: dynamic `-s` Services
through the Services-only role, the takeover repoint/restore, NodePort reach.
On Swarm the agent runs as a real **Swarm service on a non-attachable overlay**
(the prod shape): `-s` provisions its name as a Swarm-service signpost there,
and the takeover scales the deployed service to 0 and back. The 4×8 protocol
grid, multicluster, outage, gateway callback and collision run against every
family, natively from Linux, macOS and Windows. The image publishes only when
all nine legs are green.

---

## 2.0.0

### Breaking changes

- **`-s` is now mandatory when you run a command.** A running process in a cluster
  is a service, and a service has a name — so `plug` runs your command *as* a
  named member of the cluster:
  `plug [-p profile] -s <name>:<cluster-port>:<local-port> <command>`. Bare
  `plug <command>` now **errors**. **Migration:** add
  `-s <name>:<cluster-port>:<local-port>` to your invocations — name it even when
  nothing calls your process back (most of the time something will).
- **The Docker socket is required for `-s`** on Docker / Compose / Swarm (it is
  how the agent creates the name). Kubernetes uses a Services-only RBAC role
  instead. Without either, the agent falls back to a pre-declared *static* alias.
- **Needs an agent image from this release.** Against a pre-2.0.0 agent, plug now
  refuses with an upgrade hint instead of a cryptic error. With a *launcher*
  installed before this release, put `-s` after `-p`/`--host` (old launchers
  forward trailing flags they don't know straight to the core).

### New: serve a local service to the cluster (the reverse direction)

`plug -s <name>:<cluster-port>:<local-port> <cmd>` makes a local port reachable
from inside the cluster under a cluster DNS name, for the lifetime of the session
— every workload calling `<name>:<cluster-port>` lands on your machine, with **no
name pre-declared and no stack redeploy**. The agent provisions the name on the
fly: a *signpost* carrying the DNS alias — a **container** on Docker/Compose, a
**Swarm service** when the agent runs as a Swarm service (it joins the stack
overlay whether or not it's `attachable` — no network change; the agent runs on a
manager node), or a **Service** selecting the agent pod on Kubernetes
(Services-only RBAC) — created on `-s`, removed when the session ends, swept on
agent restart. The full path is verified at startup — a missing name, a too-old
agent image or a competing session **ends the session with the remedy**, never a
silent no-op — and the port closes with the session. After a transport reconnect
the mapping re-binds **and re-provisions** the name, so a restarted agent doesn't
leave it silently dead. Serving a name a real cluster service already owns is
**refused** (never a silent DNS round-robin on top of it), with the per-engine
remedy. The agent-side helper is the tunnel user's `ForceCommand` (`serve-name` /
`unserve-name` only, no shell).

e2e-proven on all three OSes with the **Docker backend** — every CI run serves a
name **declared nowhere**, then proves it two ways: a cluster workload fetches it,
AND an external caller POSTs to a **published cluster gateway** that calls the
name and round-trips a correlation id — and the full request path (root and a
deep path) — back to the runner's local service (the API-gateway use case, HTTP).
The **Swarm** and **Kubernetes** backends are coded and bench-proven but **not
yet driven by CI** (CI runs on Compose).

Security: the Docker socket is root on the host — enable dynamic provisioning
only on a trusted cluster; the Kubernetes grant is tight (Services only, namespace
scoped).

### Known limitations

- **Multi-node Swarm is unproven.** The Swarm-service backend is bench-tested on a
  single node only; `-s` relays to the agent's service VIP, which assumes the
  session's remote-forward lives on the one agent task. Run the agent as
  `replicas: 1` on a manager (global mode is refused for the same reason).
- **One agent per node.** The boot-time GC that sweeps a restarted agent's own
  orphaned signposts can, on a worker running *two* distinct plug agents, remove
  the other agent's live signpost. The shipped deploy pins the agent to a manager,
  so this needs a deliberate misconfiguration.
- **`-s` input mistakes fail loud, not silently:** two `-s` on the same
  cluster-port report "already exposed by another session?"; a duplicate name
  double-provisions. Both surface at startup.
- During an outage the transport's reconnect can briefly (≤ ~15 s, one dial
  timeout) stall other calls on that transport; already-open channels keep flowing.

---

## 1.4.0

- **Linux: the no-sudo privilege now survives cluster version changes.** The
  launcher promotes its file capabilities into the ambient set before exec'ing a
  downloaded core (they don't cross exec on their own), and the mount-ns shim
  clears them again before your command runs — no privilege leaks past plug.
  **One-time action**: launchers installed before this release lose the no-sudo
  privilege the first time their cluster changes version — re-run the cluster
  install once (`ssh get@<host> install | sh`).
- **macOS: cluster DNS is now self-healing and covers every resolver path.** A
  watchdog re-asserts plug's DNS override when macOS replaces it (DHCP renewal,
  network change — one event used to kill name resolution until `plug down`);
  manually configured DNS servers (Setup:) are overridden and restored too (they
  silently eclipsed plug for libresolv clients); and Go child binaries are routed
  to the pure-Go resolver (GODEBUG=netdns=go), killing a flat 5s-per-lookup mDNS
  detour on networks without a usable default resolver.
- **macOS: simultaneous multicluster is real (and now proven).** The global
  daemon already held one tunnel per active cluster with PID-at-connect
  attribution; docs claimed "one cluster at a time" — the CI multicluster cell
  now proves two live clusters simultaneously on macOS (same name, right
  backend), like Linux and Windows.
- **Linux: simultaneous clusters fixed.** Each launch now claims its own TUN
  device slot (plug0, plug1, …) with its own fake-IP subnet — a second
  simultaneous cluster used to die on "device or resource busy".
- **CI now tests the real user flow, end to end.** Every run installs plug FROM
  the cluster on all three OSes (the exact one-liners, real privilege grants),
  runs the 4-language × 8-protocol grid natively over a mesh, asserts
  multicluster on all three, and checks that the last PUBLISHED launcher still
  drives this branch's core. Images publish only when everything is green.

---

## 1.3.0

- **Windows: no-admin data path, validated end-to-end on a real machine.** The Windows
  data path (WinTUN + routes + DNS) now lives in a **SYSTEM service** — the SCM
  counterpart of the macOS daemon. Install it once from an **elevated Git Bash**; after
  that every `plug <cmd>` is a **non-elevated** launcher that starts the service via its
  ACL (Authenticated Users may start it) and delegates to it — proven from a genuinely
  non-elevated (LIMITED-token) process. Several clusters run **side by side**: the service
  holds one tunnel per cluster and attributes each flow by process ancestry at connect
  (validated on two live clusters + concurrent same-cluster sessions).
- **Windows: real cluster access by name.** `plug curl http://my-service:8081/…` resolves
  the single-label cluster name and reaches the service. Windows never queries a *bare*
  single-label name (LLMNR/NetBIOS only), so plug advertises a **search suffix** on the
  WinTUN adapter (`my-service` → `my-service.plug`), routes `.plug` to the in-stack
  resolver via an **NRPT** rule, and strips the suffix back — the Tailscale/WireGuard
  mechanism. (The launcher also handles the `.exe` suffix and `wintun.dll` beside a
  downloaded version.)
- **Windows installer is pure Git Bash.** `ssh get@<host> install-windows | bash -s -- <host> <port>`
  — bash, not PowerShell: a piped bash script's `exit` is reliable (a piped `powershell -Command -`
  was not, so a failed install used to run on with misleading output). The host is passed as an
  argument (`bash -s -- <host> [port]`), since the MSYS ssh streaming the script can't have its
  command line read. It fetches plug.exe **and wintun.dll from the agent** (no wintun.net
  dependency, no more intermittent fetch), sets PATH + a profile, and installs the service when
  elevated (else it tells you to re-run elevated).
- **One way to point at a cluster.** The cluster comes from `--host`/`--port` or a profile; the
  `$PLUG_HOST`/`$PLUG_PORT` environment fallback was removed (it duplicated the flags and muddied
  the precedence).
- **Windows cold-start ~15 s → ~0.8 s.** The NRPT rule goes in via the **registry** instead
  of two PowerShell starts (~3 s), the reconcile opens a cluster tunnel in ~0.3 s, and an
  idle tunnel is held for a short grace so back-to-back runs reuse it (~0.3 s). No DNS
  hijack is left in place while idle — short local names resolve normally between runs.
- **Concurrent sessions no longer knock each other out.** A channel the agent *rejects*
  (a bare name that isn't a cluster service — Windows probes plenty, e.g. WPAD) no longer
  triggers a reconnect that tore down every other channel on the shared SSH connection;
  only 1 of N concurrent `plug`s used to survive. Cross-platform (shared transport).
- **Version probe & download use crypto/ssh on Windows** (like the data tunnel) instead of
  the external `ssh` binary, which hangs on Windows when its stdout is captured over a pipe
  — that had frozen every `plug` at the version probe.
- **Attribution hardened against PID recycling.** The by-process router now stamps
  each hop of the ancestry walk with the process's start time and refuses a
  temporally impossible chain — an "ancestor" that started *after* its child is a
  recycled PID (same number, new unrelated process), so the walk aborts rather than
  misroute the flow to that stranger's cluster. This matters most on Windows, which
  (unlike unix) never re-parents an orphan, so a dead parent's PID lingers in the
  child; the guard is wired and unit-tested on all three OSes, and preps the Windows
  daemon.

---

## 1.2.0

- **Simultaneous clusters on macOS.** A single global datapath daemon now holds
  one tunnel per cluster and routes each connection to the right one by the
  calling process — so `plug -p a <cmd>` and `plug -p b <cmd>` run at the same
  time, each reaching its own cluster (Linux already did this via mount
  namespaces). Windows attribution bricks are in; its daemon is next.
- **Profiles: create by naming, no separate `init`.** Reaching a new cluster is
  just `plug -p <name> <command>` (wizard on first run, then remembered).
  `plug -p <name> -H <host> [--port <p>]` defines it non-interactively, with or
  without a command to run; `plug test -H <host>` probes an agent without saving
  anything. `plug init` is gone from the help (still works).
- **Leaner CLI help.** `plug -h` lists the everyday commands only — no
  implementation talk. The concept moved to `plug about`. `self-update` and (on
  macOS) `down` are unlisted: versions auto-update on connect, and the datapath
  tears itself down after the last `plug` exits.
- **macOS: `plug <command>` now runs without sudo (setuid-root helper).** The
  install posts the launcher as a setuid-root helper (`chown root:wheel` +
  `chmod u+s`, one sudo at install — the macOS counterpart of the Linux `setcap`),
  so day-to-day `plug <command>` needs no sudo, matching Linux. plug starts with
  euid 0 to hold the utun + DNS, then **drops your command back to your own user**
  before running it — unlike a Linux capability (dropped for free across exec), a
  setuid euid is inherited, so the child is spawned under your uid/gid and
  supplementary groups. `sudo plug` still works (it drops via `SUDO_UID`); a
  genuine root login runs the child as root, unchanged. `self-update` re-applies
  the setuid bit so an update doesn't silently disable the helper. Off localhost,
  the pinned `known_hosts` (written by the euid-0 daemon) is chowned back to you,
  so you can act on a "key changed" warning without sudo.
- **Native Windows installer.** `ssh get@host install-windows | powershell
  -NoProfile -Command -` mirrors the unix `install | sh`: it downloads `plug.exe`
  + `wintun.dll` into `%LOCALAPPDATA%\Programs\plug`, adds it to PATH, and
  pre-creates your profile — no admin needed to install (no WSL2). Launch still
  needs an elevated terminal for now (WinTUN); a persistent SYSTEM service is the
  planned "run without admin" path.
- **Kubernetes manifest modernized.** `deploy/plug-k8s.yaml` now describes the
  actual TUN data path (not the removed SOCKS proxy), with a TCP readiness/liveness
  probe and modest resource limits. `kubectl exec` transport
  was evaluated and dropped — `kubectl port-forward` already gives a zero-exposed
  port gated by API-server RBAC.
- **Multicluster (macOS/Windows) — design + attribution core.** The validated
  approach routes by PID **at connect** (not at DNS): one system resolver, fake IPs
  per name, and the flow attributed to a cluster by walking the connecting
  process's ancestry to its `plug -p X` launcher. The attribution core landed
  (isolated, unit-tested, wired into no live datapath).
  Linux multicluster already works via mount namespaces.
- **e2e coverage: WebSocket** across all four language clients (Go/Node/Python/Java).

---

## 1.1.0

- **macOS DNS fix — real apps resolve cluster names again.** A real app's
  `getaddrinfo(<service>)` used to return `ENOTFOUND` on macOS: the datapath was
  fine, but macOS resolves through mDNSResponder/SystemConfiguration, not
  `/etc/resolv.conf`. DNS is now served **at the IP layer** — a gVisor UDP
  forwarder answers `:53` on a dedicated fake IP (`198.18.<N>.53`) reached through
  the TUN — and the **system** resolver is repointed at it through each OS's
  native channel: the SystemConfiguration **dynamic store** (`scutil`) on macOS
  (`networksetup` can't touch a VPN's primary service, so it failed silently), a
  per-child private `resolv.conf` on Linux, the adapter DNS (winipcfg) on Windows.
  Proven end-to-end on macOS with an active corporate VPN. No `LD_PRELOAD`/DYLD
  interposition — coverage stays universal (Go static and gRPC included).
- **Per-instance partition.** The fake range is carved into per-instance `/24`s
  (`198.18.<N>.0/24`, DNS at `.53`, never minted), laying the groundwork for
  multicluster.
- **macOS: a persistent per-cluster daemon holds the datapath across restarts.**
  Because macOS repoints DNS machine-wide, the datapath can't die with each
  `plug <cmd>`. It now lives in a small daemon, started on demand and detached:
  `plug <cmd>` just ensures it's up, registers as a client, and runs the child (no
  tunnel of its own). Restart your processes freely — resolution survives. The
  daemon tears down and restores your DNS 30s after the last `plug` of the cluster
  exits; `plug down` stops it now; a hard kill is repaired from a DNS backup on the
  next `plug`. Linux is unchanged (autonomous per launch via mount namespaces).
- **Known macOS limits.** One active cluster at a time (the system resolver is
  global). Simultaneous *different* clusters on macOS/Windows is planned
  (transparent PID-routed, or suffix-based).

---

## 1.0.0

- **One mode: the userspace TUN, over the SSH tunnel** (`cli/internal/tun`). plug
  captures the child's cluster traffic at the **IP layer**: `wireguard-go/tun`
  opens a userspace TUN (`/dev/net/tun`, `utun`, WinTUN), a **gVisor** netstack
  terminates each TCP flow, and plug splices it to the agent **by name** (a
  loopback DNS server mints a fake `240/4` IP per cluster name; the OS routes that
  range into the device). The child's socket is never touched, so it covers
  **every runtime uniformly** — libc, Go/statically-linked, and the gRPC HTTP/2
  stacks (Netty, grpcio) that fd-level interception strands. One Go codebase for
  Linux/macOS/Windows; it needs root to create the device + routes, set up once by
  the cluster install (a root helper), so day-to-day it's just `plug <command>`.
- **The rootless fd-level machinery is gone.** The LD_PRELOAD hook, the seccomp
  supervisor, the SOCKS5/HTTP proxies and the env-proxy wiring existed only to work
  *without* root; the TUN covers everything they did and more, with far less code
  and no per-runtime gaps. There is no mode flag — the TUN **is** the mode.
- **E2E coverage matrix** (`e2e/` + `.github/workflows/e2e.yml`): a **languages ×
  protocols** grid — Go / Node / Python / Java clients, each with its natural
  driver, reaching **httpbin, postgres, redis, mongo, rabbitmq (AMQP), mosquitto
  (MQTT), gRPC** cluster services **by name** under plug. One CI job per protocol;
  the run Summary renders the full grid. Services track current majors (postgres
  18, mongo 8, redis 8, rabbitmq 4…). **28/28 green** — gRPC on the JVM and CPython
  included, the exact cases the old fd-level path could never pass.
- **`plug selftest` + native macOS/Windows/Linux CI.** A self-contained smoke that
  loops real traffic through a real TUN device **by name** with no agent and no
  Docker. CI builds plug natively on each OS, runs the unit suite, then runs the
  selftest under sudo (macOS/Linux) / WinTUN (Windows) — the visible proof that the
  data path works on each platform, not just that it compiles.
- Publishing is **CI-only**: the multi-arch image is built and pushed exclusively
  by CI; local builds are plain `go build` / `docker build`.

---

## 0.2.0

- Rootless `plug uninstall` — no sudo unless the retired root daemon left files.
- Install one-liner skips the host-key check (the agent regenerates its key at
  each start; it is not a secret) — matching what plug does internally, so a
  redeployed agent no longer breaks reinstalls.
- Local `make` builds carry the git rev (`dev+<rev>`), like CI.

---

## 0.1.0

### Data path

- Rootless, per-process tunnel to a tiny agent container over SSH
  `direct-tcpip`, so cluster DNS names resolve and services are reachable — no
  root, no TUN, no daemon; several clusters run side by side.
- Transparent `connect()` / `getaddrinfo()` / `gethostbyname()` injection (the
  "N1" hook, `DYLD_INSERT_LIBRARIES` / `LD_PRELOAD`) so any **libc** runtime
  (Node, the JVM, Python, curl…) reaches raw-TCP services — `amqplib`, `pg`,
  `mongodb`, `redis`, gRPC — with no per-service config.
- **Split-horizon routing** by name shape: single-label names → cluster, dotted
  FQDNs and `localhost` → direct, with mutual fallback. `PLUG_DIRECT` forces
  extra CIDRs / hosts / suffixes direct.
- HTTP proxy (`HTTP_PROXY` / `HTTPS_PROXY`) + SOCKS5 proxy (`ALL_PROXY`,
  `JAVA_TOOL_OPTIONS=-DsocksProxyHost`) for proxy-aware clients and the whole JVM.
- Per-session port-forwards for what the hook can't reach (Go/static, non-TCP).

### Reliability & security

- **Self-healing transport**: SSH keepalive + transparent reconnect + bounded
  channel opens — an idle NAT / VPN / LB drop no longer requires restarting plug.
- Handshake timeouts on tunnelled connections (no more hangs), preserved socket
  options (`TCP_NODELAY`, `SO_KEEPALIVE`), and a dynamic fake-IP table (no cap).
- **Host-key pinning (TOFU)** in `~/.plug/known_hosts` — a MITM tripwire on top
  of the deliberately no-secret transport.

### Install & versioning

- Install from the cluster: `ssh get@<host> install | sh` — binaries embedded in
  the installer, picked by `uname`; a per-host profile is pre-created from your
  own `ssh` command.
- Launcher model (like `nvm` / `rustup`): each cluster runs its exact matching
  version, cached under `~/.plug/versions/`. Version carries a build id
  (`<version>+<git-rev>`) so `latest` rebuilds are detected.
- Profiles in `~/.plug/*.conf`, auto-selected, with `ls` / `rm` / `rn` / `test`;
  `--host` / `--port` / `$PLUG_HOST` / `$PLUG_PORT` bypass.

### Packaging

- Docker image renamed `softwarity/plug-agent` → **`softwarity/plug`** (the
  Swarm/k8s service is named `plug`).
- Native multi-arch agent image (linux/amd64, linux/arm64); CLI for Linux and
  macOS (Windows via WSL2).

### Known limits

- **libc + TCP only.** Go / statically-linked binaries and non-TCP (UDP/QUIC)
  use a port-forward. IPv6 is treated as IPv4 (v6-only apps / v6 literals are not
  tunnelled). macOS SIP system binaries and hardened apps bypass injection (the
  env-proxy still applies). No authentication by design — trusted dev clusters only.

---
