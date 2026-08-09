# Release Notes

## NEXT RELEASE

### The cached core is run from what was verified, not from where it was verified

plug caches the core each cluster serves and checks it, on every launch, against
the digest that agent announces. That check has been there since 2.7.2. What it
could not cover on its own is that verifying a file and then running it are two
separate lookups of the same name, and the core runs with the privilege plug
holds — root on macOS, network capabilities on Linux. Anything able to write into
the cache between the two would have inherited that privilege, and what can write
there is anything running as you.

Both platforms now close that, each with the tool it actually has:

**Linux** hands the launcher the descriptor it hashed, and runs that. A
descriptor is bound to a file, not to a name, so the bytes that were checked are
the bytes that run.

**macOS** moves the store out of your home directory, to `/var/db/plug/versions`,
owned by root. plug is setuid there, so it writes and cleans the store itself:
nothing asks you for `sudo`, including `plug uninstall`. plug refuses to use the
store at all if any part of that path turns out to be writable by someone else,
and tells you the command that fixes it.

Existing installs need nothing. The store is created on first launch, and the old
cache under `~/.plug/versions` is cleared by `plug prune` — a cached core is
disposable, re-downloaded on demand and verified every time, so nothing was
migrated and nothing is lost.

**Windows** was already outside this: it elevates per launch through its service,
which runs the installed launcher rather than a cached core, so there was never a
privilege there to inherit.

### A name that is in no cluster now fails fast on Windows

Asking for a name nothing serves could take longer than the program asking was
willing to wait — it gave up on the lookup rather than being told, cleanly, that
the name does not exist.

Windows asks about one name several times at once: its search suffix turns `svc`
into a query for `svc.plug` and one for `svc`, and its resolver re-sends after
about a second while nothing has answered yet. Each of those was a separate
question to the cluster, and a name that is genuinely absent is the slowest kind
to answer — the agent has to finish looking before it can say no. Stacked up,
they outlasted the caller.

They are now one question. The answer they share is the same one, and it arrives
in the time a single lookup takes.

### What a release has been through, restructured

The three cluster families — Compose, Swarm, Kubernetes — exist to prove plug
behaves the same whichever backend provisions a name. That only reads as a
comparison if they run the same checks, and they had drifted: the same test
carried three different names, the order diverged, and two checks ran against
one family alone for no reason anyone could point at. They now run one identical
block of nineteen, in one order, and a release cannot be published if the three
ever diverge again.

A native arm64 client has its own leg rather than sitting inside the Compose
matrix, since what it asks is an architecture question, not a cluster one.

The suite also runs in about half the time — the same checks, an end-to-end run
of roughly fifteen minutes instead of twenty-eight. That is not cosmetic: the
DNS fix above had been there all along, and only became visible once nothing was
being asked at leisure any more.

### The update check no longer goes silent when the registry is out of reach

It asked the registry from YOUR machine and had no fallback: on a network that
cannot reach it — a corporate proxy, a VPN that splits routes — the check found
nothing, said nothing, and silence looks exactly like "you are up to date".

The cluster can reach the registry; it pulls from it. So plug now asks the agent
when it cannot ask itself. `plug update` always had that fallback, the
background check never did.

Needs an agent that knows the question: an older one answers `unknown command`,
and plug keeps quiet exactly as before rather than inventing an answer.

---

## 2.10.0

### The image we publish is now the image that was tested — on arm64 too

The e2e legs used to build their own copy of the agent image, so the artefact
they validated was never the one shipped. Same Dockerfile and same commit is not
the same artefact: `apk` and the wintun download reach the network on every
build, which is exactly how 2.7.3 died after a green CI.

The build now happens FIRST, carrying only its immutable `sha-<commit>` tag; the
clusters pull that; and `latest` is moved onto the very digest the nine legs ran
against, once they are green. Six redundant builds per run disappear with it.

That also closes an older gap: linux/arm64 has been built and published for
every release, and nothing ever ran it — every leg was amd64. A native arm64 leg
now runs the full Compose chain.

### The auto-update check is now exercised end to end

Written twice, pulled twice, for the same reason each time: the check runs in the
CORE, and the core is whatever the AGENT serves — so against a one-release-behind
agent it was N-1's code running, never the branch's. N-1 had no check (2.7.3),
then had a broken one (2.9.0). The fix shipped in 2.9.2, so any N-1 from 2.9.3
onwards carries it, and the cell verifies that rather than assuming it.

It now proves the whole shape on all nine legs: one session lasts long enough for
the background check to settle, dial the agent and reach the registry; the NEXT
launch is the one that announces what it found — which is the design, since the
session that finds it is busy running your command.

### Windows: the dead IPv6 resolvers are skipped, and said

Windows gives `fec0:0:0:ffff::1/2/3` to every adapter that has no DNS of its
own, so they end up in the resolver list of most machines. They never answer.
Once the real resolver goes quiet, plug tried each of them in turn — a full
timeout each — before giving up on an SRV, MX or PTR lookup.

They are now skipped. And named in the log while being skipped, because doing so
declares a whole address family "not a resolver", which will be wrong on some
network one day: whoever hits that will see the line rather than an
unexplainable failure. A real IPv6 resolver is untouched — the rule is the
deprecated site-local range (RFC 3879), not IPv6.

### Deployments following `latest` (or a branch) are checked at last

The update check answered nothing for a moving tag. There is no version to
compare — `latest` is always called `latest` — so it gave up, and anyone whose
cluster follows a stream was never told anything.

There is something to compare, though: bytes. The agent reports its running
image with the digest it resolved to, so plug now asks the registry what that
tag points at today. Different digest, different image, and it says so — with
wording that suits the case, since "update available: vlatest" would be nonsense.

### `notify` offers, instead of only telling

With a real terminal, plug now asks whether to apply the update it just found,
rather than mentioning it and leaving you to retype a command. It says what that
costs before asking — applying rolls the cluster's agent, and every other
session on it reconnects — the default is no, and it gives up after a few
seconds. No terminal, no prompt: a script or a CI job sees the message it saw
before, never a question.

---

## 2.9.4

### Fixed: an absent name took 3 seconds and came back as a fake address

Asking for a name that does not exist in your cluster should fail immediately
and honestly. On Docker Desktop it did the opposite: three seconds of waiting,
then a `198.18.x` address for a service that is not there.

The agent and the CLI had been given the same three-second budget, one waiting
on the other. The agent could not answer in time whatever it concluded, so the
CLI minted a fake address rather than stay silent — every time, not
occasionally. And the case is the normal one on Docker Desktop: the embedded DNS
forwards names it does not know upstream, upstream is your workstation, your
workstation is plugged, so the question comes back to the resolver that asked
it and nobody answers until something gives up.

The agent now answers within 1.5 s at worst, well inside what the CLI waits for.
Absent names fail immediately, and say so.

Pinning `"dns"` in Docker Desktop is no longer needed to avoid this — it goes
back to being the belt-and-braces measure it was always described as.

### `plug down` is no longer the answer to "I updated and nothing changed"

It never was. The datapath a running session uses is fixed when that session
starts; a new one is picked up simply by closing **all** your sessions — the
daemon stops on its own about 30 seconds later, and the next launch uses what
the agent now serves. `plug down` only skips that wait, and it strands whatever
is still running.

So `plug update` now says how many sessions are keeping the previous datapath
alive, `plug doctor` says to close them (with the count — believing you had,
while one was still open, is the whole trap), and `plug down` says what it is
about to cost before doing it.

`plug down` is now stated where it is a fact — on the line that says the daemon
is running — and prescribed nowhere. Even the one state where stopping is the
answer (a daemon alive but no longer answering its own resolver, which doctor
now detects instead of guessing) is handled by `plug doctor --fix` rather than
by sending you to another command.

### Fixed: `plug update` could fail at its very last step, after migrating the cluster

The agent rolls, answers "I am the new version" from its new pod — and the next
connection lands while the endpoint is still switching over. One i/o timeout
there aborted the whole update, with the cluster already migrated and only the
launcher left behind. Seen on Kubernetes, where the rollout makes the window
widest.

The launcher download now retries, because the instability is one `plug update`
causes itself. A first try that works costs nothing.

### `plug doctor --fix` repairs the resolver it was already promising to repair

A datapath that dies without tidying up leaves the machine pointed at an address
nothing answers on, and nothing resolves system-wide until someone notices.
`--fix` claimed to restore that; it never did. It does now, on macOS (the DNS
override) and on Windows (the leftover NRPT rule) — where the previous advice
was to run `plug down`, a command that could not possibly help since that state
is defined by having no daemon left to stop.

`plug update` repairs it directly when it sees it, rather than sending you to
another command for a state you did not cause.

### Fixed: `plug doctor` blamed the wrong thing, and gave a remedy that could not work

The live NXDOMAIN check reported "the RUNNING datapath predates 2.2" and advised
relaunching — on datapaths that were perfectly up to date, where relaunching
changed nothing. The symptom it saw has two causes, and it only knew one.

It now measures how long the probe took, which is the whole diagnosis: three
seconds means the existence check timed out (and names the Docker Desktop loop),
milliseconds mean the datapath never checked at all. A lookup that fails
*slowly* is no longer reported as healthy either.

The macOS remedy was wrong on its own terms: ending the sessions does not
suffice, since the daemon outlives them. It now says `plug down`.

---

## 2.9.3

### Fixed: plug follows your DNS servers when a VPN comes up or goes down

`plug doctor` now shows where lookups actually go — read from what the running
datapath published, not re-derived from the system, because those two answers
diverge exactly when it matters: a capture that went stale after a VPN moved
looks perfectly healthy if you simply ask the system again.

```
✓ dns forwarding   forwarding dotted names to 10.8.0.1:53, 192.168.1.1:53
! dns forwarding   forwarding dotted names to 8.8.8.8:53 — a PUBLIC resolver:
                   internal names will not resolve, and these lookups leave your network
```

And a resolver that stops answering is no longer fatal to the rest: a VPN pushes
two or three precisely so one can fail, and plug asked only the first. It now
tries them in order — a single sick server used to make every SRV, MX or PTR
lookup come back as "no such record".

The machine's nameservers were captured once, at session start, and kept for the
life of the session. Connect a corporate VPN afterwards and plug went on
forwarding to the resolver from before — the one that does not know your
internal names. Drop the VPN and it kept forwarding to a resolver that had gone
away. Either way the session had to be restarted, and nothing said why.

plug now follows them. Where the system's own record can be re-read honestly it
is, every ten seconds: the adapter table on Windows, `/etc/resolv.conf` on Linux
(never modified there — the repoint is a bind-mount inside the child's own mount
namespace). macOS cannot work that way, because plug overwrites the primary
service's DNS entry and a re-read would return plug's own address; it reads them
at the one moment they are visible instead — the instant the system republishes
its own servers there, just before the watchdog overwrites them again.

That last part also covers the case a VPN does not create: **changing Wi-Fi
network**. macOS keeps one network service per hardware port whatever the SSID,
so going from the office to home moves the resolvers without moving the service.
Everything looked healthy — and dotted names kept being sent to the resolver of
a network the machine had left.

Silent unless something actually moved, and unchanged servers written in a
different form do not count as a move — a session that runs all day on one
network logs nothing.

### Fixed: `plug doctor` no longer reports a data path that has stopped

`dns forwarding` kept naming the servers of the last session that ran, long
after it was over — the published record was written but never cleared. It is
dropped on teardown now, so the check appears only while something really is
forwarding.

### Proven on all three OSes: following a VPN

There is no VPN on a CI runner, so the self-test builds one: an extra address
carrying a resolver that knows a name nothing else on earth knows, announced to
the system exactly as a VPN client would — a second WinTUN adapter on Windows,
`resolv.conf` on Linux, the primary service's DNS entry on macOS. The assertion
is a fact rather than a setting: that name must resolve **through plug** to an
address only that resolver could return, and must stop resolving once the VPN
goes away. Following a VPN up and never following it down is the same bug seen
from the other side.

---

## 2.9.2

### Fixed: the update check never actually ran

`plug config update=notify` is the default, and since 2.9.0 it has announced
nothing to anybody — not because no version was available, but because the check
asked the wrong channel. Its first step requested the `version` verb over the
TUNNEL connection, where that verb does not exist: it belongs to the DOWNLOAD
channel, the one the passwordless `get` user serves. The agent answered
`error: unknown command "version"`, the check gave up on its first step, and
nothing was ever recorded — so the launch that was supposed to say "a newer
release is out" had nothing to say. Silent by design, and therefore invisible.

`info` was already answering both facts in a single round-trip — the running
version and the deployed image — so that is what it asks now.

Two more things kept it from working even once that was fixed. The check fires
while the core is still bringing up the TUN and repointing the resolver, and its
own registry lookup goes THROUGH that resolver: too early, it got "no such host"
for a name that resolves perfectly a second later. It now lets the datapath
settle first — nobody is waiting on a result meant for the next launch. And its
registry budget was the one `plug update` uses, four seconds, chosen to be
aggressively short precisely because THAT path has a fallback. This one has
none: a timeout is not a slower answer, it is no check at all. It gets room to
breathe instead.

Covered end to end in CI now, on all nine legs, which is what surfaced the bug:
an agent one release behind, a newer release on the registry, and a second
launch that must name it.

---

## 2.9.1

### Fixed: a name keeps its address across kill and relaunch — the gateway stays sane

The last piece of the "restart the gateway" story, and the measured reason it
existed: Docker's embedded DNS hands every caller a hard-coded **600-second
TTL** on cluster names, and a resolver that honours TTLs (Netty, so most Java
gateways) is entitled to keep dialling an address for ten minutes. Every
Ctrl-C→relaunch used to delete and recreate the signpost, which handed the name
a fresh address — so the gateway spent up to ten minutes dialling the old one,
and nobody waits ten minutes. No TTL on plug's side could shorten a TTL served
by Docker; the only fix that reaches every caller is an address that does not
change.

So a cleanly-unserved name now **lingers**: its signpost stays, still resolving
to the same address, refusing connections instantly — a stopped service's exact
semantics, benched at zero seconds where the old failure hung. Relaunch within
fifteen minutes and the signpost is taken over in place: same VIP on Swarm, same
ClusterIP on Kubernetes, and every cached answer out there stays *valid* instead
of stale. The grace is derived, not felt: it must outlive Docker's 600s TTL, or
the linger protects nothing. Past it, the garbage collector reaps the signpost —
at agent boot, and opportunistically on every serve — and an absent name goes
back to an honest "unknown host".

Three deliberate boundaries. A signpost carrying a parking receipt never
lingers: deleting it is what scales the parked workload back up, and an address
is not worth a deployed service left at zero replicas. A plain-Docker signpost
cannot linger: its relay target is baked into the container's entrypoint, so a
relaunch means a new container (Docker's own IPAM often re-hands the same IP —
often, not promised). And a linger is not a session: the name fails fast while
its owner is away, it does not pretend to serve.

Kubernetes gains a twin fix on the way: reclaiming a leftover Service now
patches it in place instead of delete-and-recreate, so the ClusterIP survives
that path too.

---

## 2.9.0

### Changed: the launcher follows the cluster — every direction, dev builds included

`plug update` used to refuse to touch the launcher when either side was a dev
build, and refused to move it downward. For a machine whose cluster follows the
main channel that froze the launcher for good: the agent moved, every session's
core moved with it, and the launcher stayed where its last release left it —
missing every new subcommand while politely declining to update.

One rule now, the one the cores have always lived by: the launcher matches the
agent that `plug update` was just aimed at — exactly, dev builds and downgrades
included. Wanting an earlier version is a legitimate thing to test, not a
mistake to be protected from. A downgrade is followed, not refused; it announces
the direction and the way back, because a silent downgrade is how a stale
cluster would surprise you.

One catch, once: a launcher from before this change still carries the old
refusal, so the first alignment is a reinstall from the cluster (or from
source). Every later `plug update` follows by itself.

### Fixed: killing a session no longer poisons the cluster's own DNS

Kill a plug session (Ctrl-C) on a workstation whose cluster runs in Docker
Desktop, and anything in the cluster that asked for that name during the gap —
a gateway routing to it, typically — could receive a 198.18.x address that only
exists on your machine, cache it, and stay broken until restarted. Connections
to it do not even fail fast: they hang.

The chain, reproduced live: the name disappears from the cluster, so the
embedded DNS forwards the lookup upstream — through the VM, to your machine's
resolver, which is plug while sessions run. plug's stub still held "this name
exists" in a five-minute cache, so it answered with a fake address — to a
caller inside the cluster, where that address means nothing. The 2.7.1 resolver
fix made this deterministic, ironically: before it, the stub regularly fell off
the machine's DNS and the echo missed.

Every verdict plug caches or hands out now lives five seconds, aligned in both
directions: a killed name turns into an honest "unknown host" within five
seconds (the gateway then heals by itself — no restart), and a freshly served
one appears within the same bound. The A answers' TTL drops from 30 to 5
seconds too, or the OS resolver would keep repeating the stale answer on plug's
behalf. The load stays bounded by that same OS cache in front of the stub, and
each re-check is one exec on an SSH connection that is already open.

Pinning Docker Desktop's VM DNS (`daemon.json "dns"`) remains documented
defence in depth: it kills the echo entirely, but a per-machine setting is not
a fix — this is.

### Added: `plug prune` — the version cache finally shrinks

plug caches one core per version it has ever met under `~/.plug/versions`, and
nothing ever cleaned it: a machine that follows a project for a few weeks
accumulates every release and every dev build it crossed. `plug prune` asks each
profile's agent which version it actually runs, keeps those, and deletes the
rest — on a working machine that turned sixteen cached versions into one and
freed 120 MB.

What is "in use" is the agents' answer, not a guess from file dates. A profile
that does not answer is named before anything is deleted — a version only that
cluster uses cannot be recognised as active, and you may prefer Ctrl-C over
letting it go while your VPN is down. If no agent answers at all, plug refuses
outright: being offline must not read as "delete everything". The cost of any
mistake stays small by construction — a pruned core is re-downloaded from the
agent on the next session, which is where it came from in the first place.

### Changed: a name already taken now says where the session holding it is

Refusing a `-s` name used to name the agent port holding it, which answers "is it
taken" but not the question you actually have — by whom. The agent now records
where the session reached it from, and says so:

```
"fpl-ui" is already exposed by another live session (agent port 41943, from 10.1.2.3)
```

Enough to tell your own forgotten session apart from a colleague's, which is the
distinction that decides what you do next. It is the address the AGENT sees, so
behind NAT every developer shares one and it only says "somebody out there"; a
readable machine name would need the client to send one, and that protocol change
is not made here.

Nothing else changes. An agent that predates this simply records no origin, and
the refusal reads as it did before rather than inventing a source.

### Fixed: a cluster whose DNS hiccups no longer makes a live name look absent

Before minting an address for a cluster name, plug asks the agent whether that
name exists — the check that turns an absent name into an honest "unknown host"
instead of an address that can only refuse the connect. The agent answered
"absent" whenever the lookup came back empty, including when it came back empty
because the cluster's resolver had not answered at all. A perfectly live service
could then be reported absent for 30 seconds, machine-wide on macOS.

The two cannot be told apart by the error alone: in an isolated cluster network
an absent name times out exactly like a dead resolver, because the embedded DNS
forwards it upstream and cannot reach it. So the agent now asks a second
question, about something that must exist while its DNS works — the Kubernetes
service in Kubernetes, its own deployment object on Docker and Swarm. If that
answers, the resolver is fine and the name really is absent. If it does not, the
agent says so instead of passing judgement, and plug mints as it did before.

An NXDOMAIN that actually arrives is still trusted immediately: that is the
resolver speaking. And an agent with nothing to use as a witness keeps the old
answer rather than guessing — a wrong "unreachable" would hand out fake
addresses for names that really are absent, which is the leak this check exists
to close.

### Fixed: a Swarm name keeps its address across a reconnect

plug publishes your `-s` name as a Swarm service, and Swarm gives a service its
VIP once, at creation. Re-provisioning recreated that service, so the name got a
NEW address — and every caller that had already resolved it kept using the old
one until its DNS cache expired. The name looked dead, then came back on its own,
with nothing having changed. A JVM caller shows it most clearly: its default
positive DNS cache is long enough to be very noticeable.

Re-provisioning runs after every reconnect, not only when you start a session, so
a laptop waking from sleep, a VPN switching or a Docker Desktop hiccup was enough
to move the address under callers that were working fine. The signpost is now
updated in place in that case, and the VIP survives.

Two cases still get a new address, and both are deliberate. A signpost carrying a
parking receipt is always replaced: deleting it is what scales a parked workload
back up, and an address is not worth risking a real service left at zero
replicas. And when the AGENT itself restarts, its boot gc sweeps its own
signposts before any session reconnects — there is nothing left to reuse.

Plain Docker keeps the old behaviour throughout: a container's relay port lives
in its entrypoint, so a new port means a new container. Kubernetes was already
unaffected, its Service keeping its ClusterIP across all of this.

### Fixed: Windows now forwards to your own DNS servers, not a public one

On Windows plug had no idea what this machine's nameservers were, so every dotted
name it was asked about went to a public resolver. On a corporate network that is
the worst of both outcomes: the internal names being asked about do not exist
there, and asking sends them off your network. 2.7.3 only announced it; plug now
reads the adapter table and forwards where this machine's DNS already went.

It matters more than the `.plug` suffix suggests. Windows queries the resolver of
EVERY interface at once, and plug's adapter carries one — so ordinary dotted
names reach plug too, and whichever answer arrives first is the one your
application gets.

The servers are ranked the way Windows ranks its interfaces, by metric, so a
corporate VPN's resolver — the only one that knows your internal names — is used
first. plug never forwards to its own address, on any interface: that is not an
error that surfaces, it is a query that comes back in and is sent out again for
ever.

---

## 2.8.0

### Added: plug tells you when your cluster is behind

A version could sit published for days without anyone on the team knowing. plug
now checks once a day, in the background of a session that is already running,
whether the registry carries a release the agent does not — and says so the next
time you launch.

```bash
plug config -p local update=auto     # a cluster you govern
plug config -p shared update=none    # one you do not
plug config -p neo                   # show what a cluster is set to
```

The rule lives in the profile, not on the machine: `auto` updates the agent, and
an agent is shared. Governing your own cluster while having no say over the
shared one is the normal case, so each cluster carries its own answer — and its
own record of what the registry last held. The default everywhere is `notify`.

The check runs from your machine rather than from the cluster, which is not a
detail: the same registry lookup costs about a second here and about thirty from
inside a cluster VM. It never blocks anything — the session that performs it is
already up, and what it learns is for the next launch.

`auto` rolls that cluster's agent. Every session on it drops and reconnects on
its own, and each one now says on the way back that the agent moved and that it
is still running the older core. Nothing local is swapped under a running
command: the core is holding your process, so a session keeps the version it
started with and the next launch picks up the new one. That is also why a
reconnect cannot upgrade anything — the version is chosen once, before the core
starts.

Two things it cannot see. A deployment following a moving tag (`latest`, a
branch) is left alone: whether such a tag has moved is a digest question only the
cluster can settle. And the check runs in the core, which is the version your
AGENT serves — so it starts working once the agent is on a version that has it.
From an older agent nothing is announced, including the release that first
brought this. `plug update` remains the explicit path in both cases.

### Fixed: SRV, MX, PTR and TXT lookups work again during a session

plug answered every query that was not an address with "no such record". On macOS
that is not a detail: while a session runs, plug's stub is the resolver for the
WHOLE machine, so anything relying on those records broke host-wide — Active
Directory clients, MongoDB seedlist URIs (`mongodb+srv://`), Consul, mail tools.
They are now relayed to the same upstream that already served dotted names, and
the answer is handed back exactly as it arrived, whatever record type it carries.

Names that only exist here are still answered locally and never leave: a
single-label cluster service, the `.plug` suffix Windows appends to one, and
plug's own reverse zone. Asking a public resolver about those would leak an
internal name to be told what plug already knows. AAAA also stays as it was — the
addresses plug hands out are v4, and a real v6 answer would route the client
straight around the tunnel.

When the upstream says nothing, plug now answers SERVFAIL instead of an empty
record. "I could not ask" and "there is no such record" are different things, and
only one of them is safe for a client to cache.

---

## 2.7.3

### Fixed: a long-lived daemon no longer runs out of cluster addresses

plug answers a cluster name with an address from a private /24 it owns, and that
pool was handed out once and never reclaimed. Past 254 names every later one got
NXDOMAIN — permanently, cluster services included, until the daemon was
restarted. That is not a corner case on macOS, where EVERY single-label lookup on
the machine reaches plug: a browser's anti-hijack probes alone are three random
names per network change. The pool now recycles the address left untouched
longest, and only once it is well past the TTL handed to clients, so an address
someone still holds is never reassigned under them. When everything really is in
use, plug refuses rather than aliasing two services onto one address.

### Fixed: a signpost belonging to another agent is no longer swept

Two plug agents on one host or cluster — two stacks, each with its own — collide
on the object naming a service. The liveness probe cannot tell them apart: it
asks inside its own network, where the other agent's port never answers, so a
LIVE signpost read as leftover and was deleted. The boot GC has always checked
which agent owns one; the serve path now does too, and says so instead.

### Changed: plug says when it has to forward DNS to a public resolver

With no system resolver captured, every dotted name goes to a public one — which
still resolves the internet, but not your internal names, and sends those lookups
off your network. It happened silently, and on Windows it happens by default.
Now it is announced. Capturing the real servers there is the actual fix and is
still to come.

### Fixed: a workload that fails to come back is no longer forgotten

Releasing a `-s` name restores whatever the session parked, then deletes the
signpost — and the signpost is where the receipt lives. A restore that failed was
swallowed, so the workload stayed down with nothing left recording it had ever
been parked: not even the agent's boot GC could put it back. plug now says which
containers or service it could not bring back, and leaves the signpost in place
so its receipt survives for the next attempt. Both Docker shapes and Swarm, and
the boot GC reports its own failures the same way.

### Fixed: the macOS and Windows daemon no longer races itself on shutdown

Its teardown emptied the tunnel map while a reconcile tick could still be inside
a dial and about to write to it — a data race that kills the process outright.
That process is the root daemon holding the machine's DNS, so it died before
restoring the resolver. The teardown now waits for the loop to finish.

### Removed: the `forward` profile key

It declared a local port-forward for drivers that ignored the SOCKS proxy, and
the userspace TUN made it unnecessary — capture happens at the IP layer, so
`amqp://rabbitmq:5672` works as-is in every runtime. It had been parsed and
carried all the way to the core while doing nothing at all, which is worse than
absent: it read as configured. A profile still carrying one now says it can be
deleted.

---

## 2.7.2

### Fixed: plug checks the binary it is about to run with privilege

plug caches the core matching your agent's version under `~/.plug/versions` and
runs it with the privilege it holds. It now asks the agent, over the same
authenticated channel the binary arrived on, what that file must hash to — and
checks it before every launch, not just after the download. A copy that no
longer matches is discarded and fetched again.

Two things follow. A published release names one build, so a mismatch there is
corruption or tampering and is said out loud. A `dev` or branch build
legitimately covers different bytes over time, so it is simply re-fetched — which
also ends the stale-cache surprise where a rebuilt image kept running the core
you already had.

The check needs an agent that can answer, so redeploy the `softwarity/plug`
image; plug says so plainly rather than skipping the verification. Kubernetes and
Docker desktops are unaffected in behaviour otherwise — the cost is ~30ms.

---

## 2.7.1

### Fixed: cluster names stopped resolving after a sleep, a network change or a VPN drop

macOS decides which network service is "primary", and that is where plug points
the system DNS. It resolved that service **once**, at startup, and kept writing
there for the life of the daemon — so the moment the primary changed (a laptop
waking, joining another network, a corporate VPN going down) plug was faithfully
maintaining the DNS of a service nothing resolved through any more. Single-label
cluster names went to the DHCP resolver and came back `ENOTFOUND`, for every
application on the machine at once, until the daemon was restarted.

Nothing reported it, either: the watchdog exists precisely to survive this, but
it re-checked the *old* service's key, still found plug's address there and
concluded all was well. It now re-resolves the primary on every tick, hands the
old service its original DNS back, moves the override onto the new one, and says
so in the log.


### Changed: all three cluster families run the same e2e chain

Four cells — the name lease, the two `plug update` cells and the agent-crash
resilience chain — only ever ran against Compose, because the crash simulator and
the per-leg crash-test agents lived in that cluster alone. Swarm and Kubernetes
now have them, so each family proves the same things about itself instead of
inheriting Compose's word for it, and the per-family timings become comparable.

The resilience one closes a real hole rather than adding ceremony: restoring a
parked workload after an agent restart is a *different implementation* per
backend, and only the Docker one was ever exercised. Nothing checked that a
Kubernetes agent coming back from a crash puts back the Service a session had
repointed — now something does.

---

## 2.7.0

### Changed: a name already taken by one of your own sessions offers to free it

`"web" is already exposed by another live session` was correct and unhelpful:
the holder is often a process you have no window onto — close an editor and its
terminal panes go with it, while what ran in them keeps running. plug now
records which local process serves a name. When the holder is one of yours, it
shows you what it is and offers to stop it:

```
[plug] that name is served by another session of yours:
        PID 24939, started 12m ago
        dir: /home/you/projects/web
        cmd: -s web:8080:PORT npm run dev
[plug] stop it and take the name? [Y/n]:
```

Answer yes and the session is asked to stop — its command ends, its name is
released and whatever it had parked is restored — then yours takes the name. The
offer is only made when the recorded session really is the holder (same agent
port), so a stale record can never point the question at an unrelated process,
and only ever at a session of your own: the record lives in your own `~/.plug`.

With no terminal to ask on — a script, a CI job — nothing is killed and the
refusal is simply reported. Same when the holder is elsewhere: it is on another
machine or another account, and it frees itself when that session ends.

### Fixed: releasing a name you no longer hold leaves its new owner alone

Holding a name is not forever — sleep past the keepalive and your forward dies,
the name frees itself, and the next session is granted it. When the first
session was finally stopped, its teardown deleted the signpost the SECOND one
was serving and restored a workload that session had parked, leaving it running
and unreachable. Releasing a name now says which session is releasing it, and an
agent that has since given it to someone else does nothing and says so.

---

## 2.6.2

### Fixed: your command finds `npm` again when the agent's version differs

2.6.1 narrowed plug's own `$PATH` while it holds root, and restored yours for
the command it launches — but only in-process. When the agent runs a different
version, plug hands over to the matching core, and that core inherited the
narrowed `$PATH` as if it were yours: `plug -s app:8080:3000 npm run dev` died
with `cannot start npm` on macOS, since neither Homebrew nor nvm is a root-owned
system directory. The core is now given your `$PATH`, and narrows its own.

### Fixed: a profile name can no longer name a file outside `~/.plug`

`plug rm` and `plug rn` built `~/.plug/<name>.conf` from their argument without
checking it, and `filepath.Join` resolves `..` rather than refusing it. Since
plug is setuid root on macOS, `plug rm ../../../etc/…` deleted a root-owned file
and `plug rn` moved a file you wrote into a root-only directory. Profile names
are now validated where they become a path, once, so no call site can forget.

### Fixed: one name, one live session — even with no signpost to prove it

A fixed agent port per name used to make this impossible: a second session for
the same name simply failed to bind. Allocated ports removed that, and the check
that replaced it reads the *existing signpost* — so it did not run at all when
there was none, which is exactly the state a restarted agent's boot GC leaves
behind. Two sessions could then hold one name and overwrite each other's
signpost on every reconnect, each leaving the other unreachable while everything
looked healthy. The agent now leases the name to the session that owns it,
independently of any signpost. A session refused this way says so instead of
going quiet.

---

## 2.6.1

### Fixed: plug no longer trusts the caller's environment while it holds root

plug is setuid root on macOS and carries ambient capabilities on Linux — root
once at install, never a prompt afterwards. That contract is unchanged. What
changes is that it no longer takes the caller's `$HOME` and `$PATH` at face
value while holding that privilege:

- **`$PATH`** — plug drives system helpers by bare name (`ip`, `ifconfig`,
  `route`, `scutil`, `sudo`, `lsof`…). A `PATH=/tmp/evil:$PATH` carrying a fake
  `ip` therefore ran as root, or with the `CAP_SYS_ADMIN` Linux hands the child
  explicitly. plug now resolves its own helpers against root-owned system
  directories only. **Your command keeps your `$PATH`** — it is restored in full
  for the process plug launches, so `npm`, `nest` and everything else resolve
  exactly as before. Unprivileged, nothing is narrowed at all.
- **`$HOME`** — every path plug writes under your home is derived from it, and
  `os.Chown` followed symlinks. A relocated `$HOME` pointing into a root-owned
  tree turned each of those writes into an arbitrary root write. Writes are now
  refused when the path *resolves* somewhere you do not own. Symlinked dotfiles
  keep working (`~/.plug` → `~/Dropbox/config/plug` lands in your own tree);
  only a link out of it is refused, with a message that says so.
- The version an agent reports becomes a directory name under
  `~/.plug/versions`, and then an executable plug runs. It is now validated
  before being used as a path.

### Fixed: a name's ports are re-provisioned once, not once per port

After a reconnect, every mapping of a name re-armed on its own freshly allocated
port and each one rebuilt the signpost — reading the other mappings' ports while
they were still being reallocated, and paying the rebuild N times (~8.5s each on
Swarm). One re-provisioner per name now coalesces the whole wave and sends a
single `serve-name` carrying every port. The state they share is properly
guarded; a race test covers it.

### Fixed: three silent failures around parked workloads

The invariant `-s` rests on is that whatever a session parks, it restores. Three
paths could break it without a word:

- releasing a name on Kubernetes answered `ok` on *any* error from the API — a
  timeout or a revoked RBAC read as success, leaving a Service repointed at a
  dead forward. Only an absent Service means "nothing to drop" now;
- the session teardown swallowed a failed release entirely. The likeliest moment
  for it to fail is a network already gone — exactly when you hit Ctrl-C — and
  you would walk away believing a workload came back up. It now says so, and
  what to do about it;
- the agent's boot GC, which restores what a crashed session parked, returned
  silently when it could not reach Docker or Kubernetes. It logs.

### Fixed: a signpost no longer dies on a transient accept error

One failed `Accept()` (a peer vanishing mid-handshake, a momentary fd
exhaustion) killed the signpost process — and since one signpost carries all of
a name's ports, the whole cluster name went down for the rest of the session.
Transient errors are retried; only a listener that is truly gone ends it. The
standalone container signpost also gained a restart policy, which the Swarm and
Kubernetes ones always had.

### Fixed: assorted

macOS now shows *why* a tunnel could not open (the daemon recorded it; only
Windows read it). The registry helpers mirrored between CLI and agent had
drifted: an absolute `rel="next"` truncated the agent's tag listing, and an
agent stamped `x.y.z+<rev>` (every release before 2.4.1) was mistaken for a dev
build and denied the fast client-side lookup. The CI clusters for Kubernetes and
Swarm now shut down as soon as their caller is done instead of idling out their
full TTL. The documentation site's dependencies went from 24 known
vulnerabilities to 3, all in dev-only tooling.


---

## 2.6.0

### Fixed: one `-s` name can expose several cluster ports

`-s mail-gateway:80:HTTP -s mail-gateway:425:POP3 -s mail-gateway:25:SMTP`: one
signpost now carries the name and listens on ALL its ports, each relayed to its
own session forward. Before, the last `-s` silently won the name and the other
ports fell through to nothing.

---

## 2.5.4

### Fixed: two `-s` names can share a cluster port

`plug -s neodps-mail:3000:PORT` bounced with `tcpip-forward request denied by
peer` whenever another session already exposed ANY name on `:3000`. Inside the
cluster that port is not unique — every service has its own IP, and a NestJS
fleet all on `:3000` is the normal world — but every `-s` converges on the one
agent container, where a fixed port could bind only once.

The agent-side port is now allocated by sshd per session (a remote forward on
port 0) and the signpost relays `<name>:<port>` to it, so the cluster port
stops being a bottleneck. Nothing changes in the command, for the callers, or
in what the cluster sees. Same name twice is still refused — the check moved
from the bind to the agent, which asks the existing signpost's relay port
whether its session is alive (a crashed session's leftover is still swept).


---

## 2.5.3

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
