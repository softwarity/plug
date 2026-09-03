# Release Notes

## NEXT RELEASE

### Connecting through plug on macOS was paying 92ms to ask who was connecting

Since 2.13.1 a single cluster is no longer open to every account on the machine:
before a flow is carried, plug asks which process opened the socket and whether
its user is one that registered a client. Asking cost more than anyone had
measured. The answer came from `lsof`, which walks every open file descriptor of
every process on the machine to report on one port: 92ms, on the first packet of
every new connection, which capped plug at about a dozen connections a second and
made a connection pool feel like a stall.

The same question is answered by `netstat -anv`, which reads the table the kernel
already keeps, in 3ms. Measured on the real function, not on the two commands in
isolation: 92.2ms to 3.26ms. Windows never had this problem, because it asks the
kernel directly through GetExtendedTcpTable, and macOS is now the same shape.

The reason to prefer it is not only the cost. `lsof` lists open DESCRIPTORS,
`netstat` lists live CONNECTIONS, and the difference has teeth: a process that has
not closed the descriptor of a finished connection still appears in `lsof`, in
state CLOSED, holding a port the kernel has already released and may already have
given to someone else. Three such ghosts were sitting on the machine this was
written on. Attributing a flow through one of them answers with whoever held the
port BEFORE the process actually connecting, which in the multicluster router is
the wrong cluster. A port with no live connection has no row in the kernel's
table, so that answer can no longer be produced.

One thing found while testing it, which nobody had asked for: the parser rejected
a process whose name is empty. Rejecting means "no owner", and no owner is what
the ownership check lets through. A process able to blank its own name would have
outranked the check. It is attributed like any other now, and there is a test that
fails if that guard is put back.

### A cluster belongs to one account, and now it actually does

2.13.1 said a single cluster was no longer open to every account on the machine.
Looking again at how that was enforced, it was not.

The check ran per flow: it asked which account opened the socket and compared it
against everyone with a live client marker. But a client writes its marker BEFORE
anything authenticates it. A second account on the same machine had only to run
`plug -p <the other account's cluster>`, which put its own uid in the very set it
was about to be compared against, and its traffic then went through a tunnel
opened with somebody else's key. It never needed a key, only the host and port.
And with two or more clusters up, no account check ran at all: that path walks the
process ancestry to the registered launcher and hands over its cluster without
asking whose launcher it is.

The question is asked one step earlier now, when a client registers, which is what
makes it a member in the first place. Another account holding the cluster is
refused there, by name, instead of having its connections reset by something that
cannot explain itself. Both paths are covered because both are downstream of that
marker: no marker, no membership, and no ancestor to walk to.

Two things worth knowing rather than discovering. Root is exempt, as it was
before. And Windows is not covered at all: every process there reports the same
uid, so there is nobody to tell apart, which is also why the per-flow check was
already falling through on that platform. Giving a Windows client a real identity
to record is a separate piece of work, and the code now says so where the rule is
written instead of leaving it to be inferred.

### Two allocations per packet, and a marker that could outlive its process

Every packet coming back from a cluster was allocating twice: a buffer, and the
one-element list wrapping it for the device. 165ns and 1560 bytes each time,
against 12.6ns and nothing when both are reused. The time is the small half; the
bytes were feeding the garbage collector at the rate of the tunnel.

Reusing them is only allowed if the device consumes what it is handed before it
returns, and wireguard-go does not promise that in its interface, so the three
implementations were read rather than assumed: macOS writes the buffer to the
device file and returns, Windows copies it into a WinTun ring packet, and Linux,
the one that coalesces packets and writes a header in front of them, keeps only
indices in a slice it resets on every call. None holds the memory. A test now
sends a long packet, then a short one, then a long one, and fails if any arrives
with the previous one's tail attached or if the device is handed memory that
changes underneath it.

The second half closes a window that yesterday's ownership change opened. A
client marker records a pid, and the rule reads it to decide whether another
account holds a cluster. A pid alone only says "something is alive there": a
client that crashes without unregistering leaves its marker, and the kernel
reissues pid numbers, so the first unrelated process to land on that number
resurrects a membership nobody holds. That was a small leak while the marker
merely granted access. It is a lockout now that it refuses, and it would name an
account that had already left. The marker carries the client's start time as
well, so the question is no longer "is that pid alive" but "is it still the same
process". A marker written by an older client carries no stamp and is still
trusted, because turning a version skew into a lockout would be the worse bug.

---

## 2.13.2

### Seven more functions that existed twice, and one that only looked wrong

The readiness and error markers for a cluster were written out in both the macOS
and the Windows file, under a build tag that already covers the two. The code was
identical; what differed was what each copy explained. One described the daemon
and mentioned mirroring the other, the other described the service and was alone
in saying why the file has to be readable without privilege. Whoever read one
learned less than whoever read the other, and neither said so. Both explanations
survive in the single copy.

Their four neighbours stay where they are and genuinely differ: taking the lock,
telling whether a daemon is alive, where the known hosts live, where the ready
marker goes.

Alongside it, something that was reported as an inconsistency and is not. The CLI
requires a patch release of Go where the agent and the e2e modules ask only for a
minor one, and that is because gvisor declares that minimum: the netstack is not
optional here. Changing it looks like it works and `go mod tidy` puts it straight
back, silently. It is left alone and the reason is now in the file, so the next
reader does not spend the same half hour.

And the two actions GitHub has begun warning about are on their current major.
Checked rather than assumed: the version that clears the warning only changes the
runtime, the one after it only changes dependencies, and the single behaviour
change in it concerns a Java distribution this repository does not use.


### The last five CI warnings all came from one step

Five warnings sat on an otherwise green run and read as five problems in four
places. They were one: the step that joins the tailnet. The version we were on
built Tailscale from source on macOS, which dragged in a checkout action still
running on Node 20 and ran a Homebrew install, and the runners now warn that an
untrusted tap sits in their image. The current major installs a prebuilt binary
and does neither, so all five go at once.

It is not a free upgrade, and the interesting part is not the warning. On macOS
the new version points the system resolver at MagicDNS once the tunnel is up, and
the flag we pass to keep Tailscale away from DNS does not prevent it: the flag
tells the client not to touch DNS, the action then does it anyway, afterwards.
That resolver serves the tailnet and nothing else on a CI runner, so the machine
lost every public name. The three macOS legs died on the following step, fetching
an artifact, with someone else's ENOTFOUND and no mention of DNS anywhere. The
resolver is handed straight back now, and the step that does it proves the name
resolution works again through getaddrinfo rather than leaving the next action to
discover it does not.

---

## 2.13.1

---


### A single cluster is no longer open to everyone on the machine

plug's daemon is machine-wide. On macOS it repoints the primary network service's
resolver, so every process on the box, under any account, resolves a cluster name
and gets back a fake IP that connects. With two clusters up, the router already
walks the connecting process's ancestry and refuses a flow it cannot attribute.
With one, it took a shortcut: no ambiguity, so no question asked. A second local
account could reach another user's databases by typing their name.

The shortcut now asks one question, which account this flow belongs to, and
refuses only when it has positively established that the answer is an account
with no client on this cluster. An unreadable socket table, a process that
vanished, a client too old to say who it belongs to: all behave exactly as they
did before. A single-cluster session is the main path, and a datapath that starts
refusing on a bad second would be worse than the leak it closes.

It does not pretend to stop code running as you. That code can run plug itself.

### Windows: the SYSTEM service will not open just any file as a key

The service reads its cluster address, and the path of the key it dials with, out
of a directory the installer deliberately makes writable by users. The guard that
vets such a path on macOS and Linux was an empty function here, on the reasoning
that Windows never inherits a root identity. True of the launcher, false of the
service.

A key must now be a regular file, which rules out a pipe or a device where
opening is not reading, and it must not live under a system directory, which is
the only case where the service's privilege buys an attacker something they could
not do themselves. That left one case, and it is closed too now.

A user could still name a file inside ANOTHER user's profile and have the machine
account open it. The answer could not come from anything the client wrote, since
the same user wrote it. It comes from who OWNS the marker the client registered:
Windows records the creator of a file and a caller cannot make it name somebody
else, so the account behind that client is known for certain. Its profile
directory is read from a machine-wide registry key only administrators can write.
A key path outside that profile is one the client has no business naming.

Every lookup behind it fails open. An owner it cannot read, a profile it cannot
find, and nothing is refused, exactly as before this existed: refusing on a
lookup that did not work would turn a service that reads one file too freely into
a service that reads none.

### Two licences were missing from THIRD_PARTY_LICENSES.md

The file says of itself that it is generated from the actual link graph. It was
not: `golang.org/x/term` is linked into all three builds and
`golang.zx2c4.com/wireguard/windows` into the Windows one, and neither was named.
Both licences require their notice to travel with the binary form, and plug ships
those binaries from the agent image and the install script. A test now asks the
toolchain instead of trusting a reading.

---


### The core store was judged one directory too shallow

macOS runs the cached core as root, so the directory holding it is checked before
anything is executed out of it. That check stopped at the deepest component that
already existed, reasoning that if this one is sound then what we create below it
is ours. It only holds if the ancestors are sound too. A tidy root-owned
`versions` sitting inside a world-writable parent passed, and whoever could write
that parent could move the directory aside, put their own in its place, and have
root execute what they left there.

It now looks at every existing component up to the filesystem root. Four extra
stat calls on a path five deep.

Found while writing the tests for that guard, not by reading it: its refusal
branches had never been taken by anything, so neutralising it entirely left the
suite green. They are taken now, by twelve deliberate breakages that each turn a
test red.


### The reconnection nobody asks for was the one that said nothing

A laptop wakes, a VPN blinks, the keepalive notices the connection is dead and
replaces it. When the re-dial failed the user was told; when it worked, nothing
was printed, so the last thing they had read was still that the agent was
unreachable.

The line hung off holding a stale connection to close, and the keepalive path
never holds one: it closes and clears the dead client first, precisely so a
failed re-dial does not leave the next tick pinging a zombie. So it announced
every reconnection except the automatic ones. An exposing session was covered by
a different message from elsewhere, which is probably why this lasted.

### Two things the tests found that reading had not

The forward that an agent opens is checked for a usable port, and when the check
tripped on a port of zero the message ended in `<nil>`: it printed the error from
a step that had succeeded. Two separate failures now say what they are.

And a comment claimed the re-provisioning hook ran in the background so the
accept loop stayed live for the verification nonce. The requirement is real, the
background was not: it is called inline, and it works only because the one hook
that exists is a non-blocking send. The requirement now sits on the function that
registers a hook, where whoever writes the next one will read it.


### plug update grants the privilege before the binary is in place, not after

Replacing the launcher wrote the new binary, moved it into place, and only then
asked sudo to make it setuid root, or to re-apply its capabilities, naming the
destination by PATH. plug installs into ~/.local/bin, which the user owns, so
anything running as them could put its own file there in between and have sudo
grant IT the privilege. The grant now happens on the new file before it is moved,
so the inode that arrives is the one that was written and there is no in-between.

On macOS it no longer asks sudo at all: plug is setuid there, so the process
doing the update is already root and can do it itself.

And the command is no longer built as a string for a shell. It interpolated the
path between single quotes, so an install path containing an apostrophe closed
the quote and the remainder ran as root. Nothing there needs a shell, so there is
none.


### The two copies of the registry client had drifted, and nothing was watching

`cli/registry.go` says in its own header that its helpers are mirrored from the
agent's and must be kept in sync. Nothing checked, and four behaviours in the
token exchange had come apart, each of them the CLI being the weaker copy: it
matched the `Bearer` scheme case-sensitively where the standard says the scheme
is case-insensitive, so a registry answering `bearer realm=...` worked from the
cluster and not from here; it parsed an auth server's error page as a token
document, so a refusal reached nobody; a document with no token at all came back
as success, and the retry then went out with an empty `Authorization: Bearer`
behind which there is nothing; and it forwarded every parameter of the challenge
to the token server, including the registry's own diagnostics.

The repository already had the dispositif that would have caught this, guarding
another mirrored function, and that one did not drift. This one now exists too.
It compares parsed function bodies rather than searching for text, so an inverted
argument is caught as well. Where the two are written differently but do the same
thing, the pair is pinned by fingerprint instead of being rewritten: editing
either side moves the fingerprint and asks whoever edited it to confirm they
still agree.


### The netstack is sixteen months less old

gvisor is the userspace network stack every intercepted flow goes through, and it
had been pinned since May 2025 with nothing saying why. Nothing was wrong with
it; a pin with no reason attached simply reconducts itself forever.

The upgrade needed one change: the UDP forwarder's handler now reports whether it
handled the packet, where before it returned nothing. It answers yes, which is
what the forwarder used to answer unconditionally, and is also right on its own
terms, since every packet arriving at that address is a query for us.

How to do it next time is now written in go.mod, because getting it wrong costs
an hour: gvisor publishes no tags, and `@latest` resolves to a master whose stack
package carries a test file declaring a different package, which the go tool
refuses outright. The branch meant for module consumers is `go`.

The upgrade also pulled in a new transitive dependency, and the licence test
written the same afternoon caught it before anyone else could.

### Dead code that a guard was watching

Three copies of the relay loop were compared against each other to catch drift,
and one of them, in internal/tunnel, had no callers at all: both deadcode and
unused reported it on every platform. So a third of that guard's attention went
to code nothing ran, while the live splice sitting beside it was guarded by
nothing. That one now has its own tests, the dead copy is gone, and the guard
counts its copies so one going missing fails instead of quietly comparing less.

Also removed: a constant naming a path the agent already names itself, a one-line
wrapper nobody called, and an interface with a single implementer whose second
implementation was never instantiated, not even by a test.


### A message the CLI reads was written six times

When a name is already served, the agent refuses and names the port the holder
sits on. The CLI does not just print that sentence, it PARSES it: finding the
port is what proves a local record really is the holder and not a leftover naming
a recycled process, which is what makes it safe to offer to stop it. That
sentence existed as six copies of a format string across three backends, five of
them out of sight of the sixth. One losing its second field would have broken the
offer on one backend and left the other five working.

One constant now, and a test that takes the agent's own text, renders it, and
runs it through the parser that has to read it. The first version of that test
passed against a refusal that had lost the field, because Go renders an unused
argument as trailing "%!(EXTRA ...)" and the parser found what it was looking for
in there. It is the second version that catches it.

### Smaller things

The two e2e Go modules carried four vulnerabilities that were not merely present
but actually called, including an authorization bypass in gRPC. They are on
current releases now, and their versions of the shared libraries match the ones
the CLI and the agent use.

`uninstall` deletes trees derived from the home directory while running as root
on macOS, and it was the one privileged path that did not first ask whether those
trees belong to the user. Every other site that writes as root under a user path
asks; deleting is not the operation where it stops mattering.

And the last helper still resolved through `$PATH` now goes through the same
lookup as its neighbours. It was safe today only because its caller happens to
arrive with a reduced `$PATH`: an invariant held by a coincidence between two
guards rather than by construction.


### The coverage matrix said nothing to anyone who could not see it

Its 111 cells carried their state in a glyph and a colour, so a screen reader
announced "!" or a dash, and the legend explaining them sat fifty lines away
linked to nothing. Each cell now carries its word, works or partial or not yet or
not applicable, next to the glyph rather than instead of it, and every cell is
named by its feature and its platform. The other tables on the site gained the
same header markup.

The diagram on the home page looped for as long as the page was open, with no way
to stop it: the animation is SMIL inside an embedded SVG, which CSS cannot reach
and cannot switch off. The deployment already renders a still frame of it, and
that is what a reader who has asked for reduced motion gets now. If the still is
ever missing, and it is absent from a local build by construction, the page falls
back to the animated one rather than showing a broken image.

While in there: a service that fetched the released version at runtime was
deleted rather than fixed. The job it claimed is already done at build time, and
better, by the step that pins the tag into the manifests the site hands out, so
what a reader copies and what they download already agree. Two navigation
landmarks got names, the header and footer became real landmarks instead of being
buried inside the main one, and a page carried 46 lines of styles for markup it
does not have.

### The attribution tests skipped instead of failing

The tests that exercise the OS primitives deciding which cluster a flow belongs
to, the process table and the socket table, skipped when the primitive answered
nothing. go test says nothing about a skip unless asked, so breaking attribution
outright left the package green and silent. That was measured. A CI runner has
those tools; there, an unanswerable question is now a failure, which is the shape
the Windows elevation tests already used.


### Two tests that asserted nothing

One of them checked that a truncated download in the core store is refused, which
matters because macOS runs that binary as root. It wrote its fixture under the
home directory while macOS keeps the store in /var/db, and it pointed at a closed
port, so the branch carrying the size floor was never reached. What it proved was
that a dead port returns an error. Removing the floor left it green, and passing
in no time at all because it never got as far as a download.

The other three were gated on environment variables that nothing in the
repository has ever set, so they had never run anywhere; and had they run, one
logged the status code it received and the other checked that a slice was not
empty. They stood in for coverage of the tunnel's data path without providing
any.

They are replaced by tests that run everywhere, against an SSH server in the same
process that actually forwards what it is asked to forward. One asserts that a
name existing only in the cluster answers with its own bytes, and that the agent
was asked for that name verbatim rather than for something resolved on this side
first, which is the difference between a tunnel and a proxy the application has
to know about. The other asserts that the resolver asks the cluster's DNS over
that same tunnel, since a lookup falling back to the host's resolver would
succeed on some machines and fail on others.

That second one waits for the routing decision rather than for the answer. Its
first version waited for the lookup to return, and when the routing was
deliberately broken it took sixty seconds to fail and reported it as a test
binary timeout with a goroutine dump instead of saying what was wrong.


### An answer the agent could not read came back as a success

The agent talks to Docker and to Kubernetes through two helpers that had the same
twenty lines at the bottom and had stopped agreeing on them: the Docker one
returned the error when it could not decode the reply, the Kubernetes one dropped
it. So a Kubernetes answer the agent could not parse came back as "200, all
fine", with the output left at its zero value, and the caller acted on that. A
Service read as having no selector and no ports is indistinguishable, from there,
from a Service that really has none, and the repoint that follows is made on
nothing.

There is one such helper now, so the two cannot disagree again, and it has tests.

### The resolver could be taken down by a query it was built to receive

plug's DNS answers on an address the machine's own resolver forwards to, on
macOS for the whole system, so anything on the box can send it anything. The
parser is properly defensive and always was, but nothing said so: its one test
parsed a single well-formed query, so every bound could have been removed and it
would still have passed. Thirteen malformed shapes now go through it, from an
empty packet to a compression pointer it does not implement, and none of them may
crash it.

Writing those found something the reading had not. The function follows a pointer
to the upstream resolver in two places, nothing declares that pointer as
mandatory, and a caller wired without one would have taken the datapath down on
the first query for a name plug does not own, which is to say almost immediately.
It answers SERVFAIL now, which is what is true: there is no upstream to ask.

### Two labels the agent reads in four places were written by hand

The label marking a signpost, and the one naming its owner, were string literals
at six sites, two writing and four reading, six hundred lines apart, while every
neighbouring label in the same file was already a constant. A typo in one of the
four readers makes another agent's signpost look like a residue belonging to
nobody, and the boot sweep removes exactly that.


### Five functions that existed twice, and had started to differ

Counting live sessions, waiting for a cluster to come up, reporting which
clusters have clients, saying where dotted names are forwarded, and running the
autonomous datapath: each was written out once per platform, in files that share
a build tag the repository already uses elsewhere. Nothing in any of them is
platform-specific; every function they call is itself declared for both.

They had begun to drift in the way copies drift, not in behaviour but in what
they explain. The macOS copy of one carried a paragraph on why it reports the
daemon's recorded reason for a failure, and the Windows copy had the same code
with no explanation. Whoever read the second learned less than whoever read the
first, and a fix applied to one would have missed the other.

### Three things the harness could not have caught

`kind` was installed from `latest`, with no checksum and no retry, written as
root into a system directory and then run, on the critical path of the three
Kubernetes legs. An upstream release could have turned that family red on a day
nobody touched this repository. It is pinned, checksummed against a digest, and
retried, which the image build already did for the WinTUN driver and for the same
reason.

The predicate deciding whether a probe answered with an address or with an error
only asked whether the string was made of digits and dots, which "...." and "999"
both are. Two truncated replies from a probe that had stopped answering properly
would have compared equal and read as "the address was kept". It now wants four
numbers, each in range.

And five background sessions were awaited without a bound, in cells that rely on
a session honouring its own time limit. One that did not held the leg until the
job timeout, twenty-five minutes later, with nothing in the log naming what was
being waited for. The bounded wait the rest of the file already used now covers
them too.


### The e2e clusters pulled their service images anonymously

A compose cluster died on `rabbitmq Error unauthorized: authentication required`
while pulling one of the ordinary service images the mesh is built from. That is
Docker Hub's anonymous rate limit, shared by every project running on the same
runner address. The four legs waiting on that cluster then spent thirty minutes
reaching their own timeout to report that the cluster never came up: a message
pointing at this repository for something that happened at the registry.

The credentials already existed and were simply not used there. Absent, on a
fork, the pull stays anonymous exactly as before.


### A stuck e2e cell now says so instead of vanishing

Everything inside a cell was already bounded: the plug sessions carry an alarm,
the waits go through a bounded helper, every request has a deadline. The cell
itself was not, and that is the shape the failure kept taking. A Windows leg sat
in one cell for thirty-five minutes, the runner cancelled the job, and the log
ended mid-cell with the fifteen cells after it never run and nothing saying which
was to blame. It has happened on three different cells now, which is why the
answer belongs at the dispatcher rather than in whichever cell it lands on next.

Twelve minutes, where the slowest cell finishes in about two: not a performance
budget, the line past which a cell is not slow but stuck. Crossing it prints what
the shell was doing, the live sessions with their elapsed times, and the tail of
each session log, then fails the leg by name.


### The version stamped in an image and the version printed on it were never compared

The job that publishes the semver tags carried a comment saying the two cannot
disagree, same commit, same build, one truth. The reasoning holds only if the
build saw the tag, and the build job has no dependencies: it starts within
seconds of a push while the promotion runs half an hour later. A release commit
and its tag arriving as two separate pushes puts that entire window between them,
and the image is then stamped with a development version and published as the
release. Nothing downstream could tell, and every launcher fetching that core is
told a version its own bytes do not carry.

The promotion now reads the version out of the image it is about to name, and
refuses if they differ. It costs one pull of an image the job has already
authenticated for.

### An installer that reported success while shortening your PATH

On Windows, `setx` truncates silently past 1024 characters: it warns on stderr
and exits 0. The installer sent both to /dev/null and announced "added to PATH",
so on a machine with a well-populated user PATH it could remove everything
installed before plug and say it went well. It now checks the length before
writing, and keeps the output so a failure can say what it was.

Also in this pass: the documentation site's dependency tree is clean again,
without leaving the major version it declares; the one floating dependency among
the node e2e client's eight is pinned like its neighbours; and the TODO's status
line names the version that is actually out.


### The orphan cluster reaper is wired at both ends now

A cluster serving an e2e run leaves early when it sees that run finish, and it
found the run to watch by splitting the correlation id. That split broke silently
the day the id grew a field: every cluster then ran out its full timeout, holding
a runner from a pool of twenty, with the early exit disarmed and nothing saying
so. The split was fixed, and it remains as the fallback; what was missing is the
value it exists to guess. The caller now states its own run id when it dispatches
the cluster, and a value passed outright cannot be parsed wrong.

### A test that failed with an index out of range instead of a sentence

One of the three tests that read source rather than behaviour cut a file from a
function's name to the next closing brace, using an index that returns -1 when
the function is renamed. The slice that follows then panics, so the test reported
an index out of range rather than the thing it exists to say. Its two siblings
guard their extraction; this one relied on the panic. It parses the file now, and
a function that moved is told to take its test with it.


### Repairing the machine's resolver after a crash was covered by nothing

When the macOS daemon dies without unwinding, killed outright or out of memory,
the next launch puts the system resolver back from three saved pieces. That path
had no test at all, so the order it replays them in was whatever the code
happened to do.

The order is the whole thing. The service dictionaries have to go back before
plug's global override is dropped, because the system recomposes the global one
from them: drop it first and there is a window with no resolver at all, on a
machine whose owner has just had plug die under them. It also has to remove its
own saved copies, or the launch after that replays a resolver the machine has
since moved on from, undoing whatever was done in between.

Both are asserted now, along with the case where a saved piece is empty, which
means the service had no DNS of its own before plug touched it: putting an empty
one back would pin that service to no resolver at all, so the entry is removed
instead.


### The agent's command entry point is readable again

Every command arriving over SSH lands in one function, which validates it and
then does the work: all seven verbs inline, 250 lines, the highest branching
count in the repository by some distance. Reading what `resolve` does meant
scrolling past everything `info` does, and the validation that stands between the
network and the cluster was interleaved with six implementations of six different
things.

Each verb now has its own function and the entry point is twenty lines, so the
checking is visible at a glance. Nothing else changed, and that is checked rather
than claimed: the hundred statements it held are the same hundred, in the same
order, from the opening guard to the closing default.


### A test that named the wrong subject

One test claimed to cover the shared state of an exposing session as a whole. It
took and released the lock in the test itself rather than calling the functions
that do, so removing the mutex from all three writers left it green; removing it
from the single reader is what fails it. It is named after the reader now, and
the writers are covered by the test that drives the re-arm wave through them.

Both are kept. They fail on different mutations, and a test that names its
subject correctly is worth more than one test fewer.

Housekeeping in the same pass: two dependencies the documentation site declared
and never used are gone, one of them a syntax-highlighting theme the site carries
its own copy of; and the tests for one source file, which had been living in two
files with no boundary between them, are in one. There was no answer to where the
next one should go.


### An agent answering is not a cluster ready

The multicluster cell reaches two clusters at once and asserts each name lands in
the right one. It waited for the second cluster's AGENT to answer, then asked it
for a service, and on Kubernetes the agent is serving well before the deployments
behind it are: the cluster was up for three minutes, its agent answering, and the
service returning nothing.

The retry it had was two attempts five seconds apart, and it could not simply be
made longer: the whole point is to reach the second cluster WHILE a session on the
first is alive, and that session lives for eight seconds. So the waiting moved
ahead of the timed part. Both clusters must actually serve before the assertion
starts, and a cluster whose agent is up but whose services never come says
exactly that instead of failing as a routing error.

### Each orchestrator the agent speaks to has its own file now

Three of them lived in one file of three thousand lines, interleaved: reading how
a Kubernetes Service is repointed meant scrolling through how a Docker container
is parked. They are three files, and what is left is the part that belongs to
none of them: the SSH verbs, the name leases, the signpost. Three thousand lines
to sixteen hundred.

Nothing changed and nothing could, moving a function between files of one package
being invisible to the compiler. Checked all the same, the way the command
dispatcher was: the same 127 declarations before and after, and not one line of
code lost, the only additions being the imports each new file needs.

### The Linux half of the attribution had no test, and the site said it had

The primitive that maps a connection back to the process that opened it exists
for all three platforms; the router that uses it is built for two. So the Linux
implementation had no caller and no test, while the coverage matrix listed it as
working and unit-tested everywhere. Rather than delete an implementation the day
the Linux daemon needs it, the claim is now true, including the case where a port
nobody holds must answer "no" rather than a plausible pid: a wrong answer there
does not fail a lookup, it routes somebody's traffic to the wrong cluster.


### A test helper promised something it did not deliver

A helper named for stopping a background watcher waited until that watcher's read
counter stopped moving, and called it stopped. Those are different things: the
last iteration can be past its read and still inside the call that publishes
where it forwards, which reads a path from a variable the test restores on the
way out. The race detector caught the pair on a CI runner, on a commit that had
touched none of it.

It waits on the goroutine's own exit now, which is the only edge that means what
the helper's name claims. Said plainly: the window is small enough that twenty
local runs with the fix removed do not reproduce it, so what is offered here is
the race report naming both goroutines and the shared variable, not a local
reproduction.


### On Linux, any local account could replace the machine's resolver

plug enters a private mount namespace for your command so the resolver it points
at is yours alone, and it does that by re-executing itself through a hidden verb
that bind-mounts a file over /etc/resolv.conf. Inside the namespace the parent
creates for it, that is exactly right. Nothing checked it was inside one.

Run from a shell, the verb is in the MACHINE's mount namespace, and both mounts
still succeed: plug carries CAP_SYS_ADMIN as a file capability, and file
capabilities are granted on exec whoever does the exec'ing. So any account on the
box could point every other account's resolver at a file of its own, using plug's
privilege rather than its own.

It makes its own namespace now, before touching anything, and that is stronger
than checking for one: from the normal path it copies an already fresh namespace
and changes nothing, and from a shell it contains the mount to a namespace it has
just created, which nobody else can see.

Detecting the situation was the first instinct and it was wrong. Comparing our
mount namespace against the parent's means reading the parent's entry under
/proc, and the parent has raised capabilities, which makes it unreadable even to
the same user. All three Linux legs said so within the hour.

Its neighbour, the verb that starts the macOS datapath daemon, was reported
alongside it as the same kind of hole and is not one: that daemon is machine-wide
by design and any ordinary `plug <command>` already starts it. Refusing the verb
would change nothing an attacker could not get by running plug normally. What
bounds that one is not who may start the daemon but what a flow may reach once it
is up, which was the single-cluster ownership check earlier in these notes. Both
findings are now written where the code is, so neither is re-raised as the other.


### The path that was checked and the file that was opened could be two different things

Before reading a personal key, plug checks that the path belongs to the user it
is acting for, and then opens that path again. Between the two, anything running
as that user can replace the final component with a link to a file only root can
read. plug is setuid on macOS, so the second open is root's: the file comes back,
and is offered to whatever server the caller pointed it at.

The read now refuses to follow a link at that component, and takes the ownership
from the descriptor it already holds, so what was checked and what was read are
the same file rather than the same name.

What this does not close, said plainly rather than implied: an ancestor DIRECTORY
swapped between the walk and the open. Closing that needs a resolution mode that
exists only on recent Linux, and this code also runs on macOS.


### A datapath that dies now says so

The bridge between the network device and the stack is the datapath. When its
read side ends, packets keep going in and nothing comes back: every name still
resolves to an address that now answers nothing, so the session looks alive and
reaches nothing. It used to end without a word, and the ordinary way it happens
is a laptop waking with the device gone from under it. It reports what happened
and what to do about it, and a clean shutdown stays silent, since that is how a
session ENDS rather than how it fails.

The other half, writing replies back to the device, threw its errors away. It
says the first one and then stops repeating itself: a device refusing one packet
refuses the next too, and a line per packet would bury the first.

### A signal arriving too early took the launcher down

plug relays a termination signal to the command it is running, and started
listening for one before that command existed. A signal landing in the window
between dereferenced nothing and panicked, instead of being passed on. Microseconds
wide, and reachable by anything that signals plug the moment it starts, which is
what a shell doing job control does.

Its twin, further down, needs no such guard because it starts after the process
exists. That difference is now written where the two can be compared, so the
unguarded shape is not copied.

### And the background update check said nothing when it gave up

Never taking the session down is right, a background nicety must not. Swallowing
the reason made the only thing that can go wrong there invisible: the check
simply never happens, for weeks, and the single symptom is a version notice
nobody ever sees.


### One quiet resolver made every lookup take four seconds

A machine is usually given several DNS servers precisely so that one of them can
fail. plug asked them strictly in turn, with a four second budget each, so a
single unreachable resolver, which is what a VPN transition routinely leaves
behind, made EVERY dotted name take four seconds, and two of them eight. Client
libraries give up well before that, so a machine whose primary resolver had gone
quiet looked like a cluster that would not answer.

They are staggered now: the first server gets a fifth of a second alone, and the
next joins in only if that one is slow. The healthy case still asks exactly one
server, so the extra queries exist only when they are needed, and a server that
refuses outright brings the next in at once rather than waiting out a delay it no
longer shares.

### The agent paid a TLS handshake on every call to Kubernetes

It rebuilt its API client, and with it the connection pool, every time it spoke
to the cluster: on every serve, every unserve, every boot sweep, every repoint,
several times each. Built once now. The service account token is still read on
each call, deliberately, because the kubelet rotates it and a cached one stops
working mid-session.


### Asking three clusters whether they know a name cost three times as long

Before minting an address for a short name it has not seen, plug asks the clusters
it is attached to whether any of them holds it. It asked them one after another,
and each question is bounded at three seconds by the agent's own budget, so a
laptop attached to three clusters where two were reachable but sluggish paid nine
seconds. That sits on the resolution path, so the time is paid by whatever was
just typed.

They are asked together now. Unlike the upstream DNS servers, where every extra
server asked is extra traffic to somebody else's resolver, each cluster here
receives exactly one question either way and they are different clusters: nothing
was ever gained by spacing them out. A cluster that holds the name ends it at
once, and when none does, every reply is still waited for, because "nobody has
it" and "nobody could answer" lead to different decisions and only the count
tells them apart.


## 2.13.0

### A copy of the relay loop had quietly lost a branch

The function that splices two connections together exists three times: twice in
the CLI and once in the agent, which is a separate Go module. The duplication is
deliberate, since sharing it would mean publishing a package for fifteen lines.

They were supposed to be identical and one of them was not. The copy in the
datapath had lost the branch that closes a destination unable to half-close, so
a direction that finished copying signalled nothing and the other could wait on
an end-of-stream nobody was going to send. It does not happen today, because
both ends there can half-close, which is precisely why it survived three copies
and every reading of them.

Aligned, and now compared by a test, the way the three end-to-end blocks are.
Drift is silent; nothing else fails when it happens.


### One unreachable cluster no longer freezes the others

Only relevant if you run several cluster agents at once, which is the only
situation the macOS and Windows datapath daemon exists for.

The daemon reconciles every 300 ms and opened each missing tunnel inline, with a
15-second timeout on the connection. So one agent being down or slow parked the
whole loop for those fifteen seconds: no other cluster got its tunnel opened, no
finished tunnel got closed, and the next pass simply waited its turn behind the
one that was never going to answer.

Connections are now started and left to run. A cluster already being dialled is
not dialled again, which matters at three passes a second: a connection that
takes fifteen seconds to fail would otherwise have forty-five copies of itself
in flight before the first one gave up.

### The documentation site: what a keyboard and a screen reader get

Three things it did not do, all of them things a mouse user never notices.

**The page title never changed.** On a single-page site the browser tab, the
history entry and what a screen reader announces on arrival all come from it,
and it read "plug" from the first page to the last. Every page now has its own.

**Focus stayed on the link you clicked.** Navigating put the new page on screen
and left the keyboard where it was, in the sidebar, so reaching the content you
had just asked for meant tabbing through the whole menu again. Focus now moves
into the page, and the page scrolls back to the top.

**A file block's header was one control containing two others.** The whole row
announced itself as a button, with Copy and Download nested inside it, which is
invalid: a control cannot contain controls, and `aria-expanded` on the row
claimed the download button was part of expanding the file. The row is now a
plain container with a real button around the part that expands, which also
means the keyboard handling is the browser's rather than ours.

**And the site no longer waits on a fetch nobody reads.** A version resource was
loaded before the first render, blocking it for up to two seconds, to fill a
value no page displays. It loads in the background now.


---



### The WinTUN driver is checked before it is unpacked

The Windows datapath loads a DLL fetched from wintun.net at image build time,
into a process running elevated. It was taken on faith: TLS says who answered,
never what they said, so anyone able to answer for that host during a build put
code into that process.

Its SHA-256 is now checked before anything is unpacked, in the image build and
in the TUN selftest both. To be exact about what that proves: the digest was
taken from the archive a build fetched, so it does not certify what upstream
published. It certifies that those bytes cannot change without someone editing
that line, which is the property that matters when a version number names one
release.

The two third-party actions in the pipeline are pinned to commits rather than
moving tags. One of them runs with the release token in scope.


---



---


### The core is now signed, and the launcher checks the signature before running it as root

plug runs the core with the privilege it holds: root on macOS, CAP_SYS_ADMIN on
Linux. Which core it runs came from the agent, and which agent it asks is chosen
by whoever launches plug, with `-H` on the command line or a `host =` line in a
profile file the user owns. The only thing vouching for those bytes was a SHA-256
announced by that same agent, which proves the download was not corrupted in
flight and nothing else. Code running under your account, with no privileges of
its own, could stand up an SSH listener on 127.0.0.1, answer three questions, and
have its binary executed as root.

The release workflow now signs every published binary with an ed25519 key whose
public half is compiled into plug and whose private half never leaves the build.
The launcher verifies that signature against the digest it measured itself, on
every launch, on the cached core as well as on a fresh download, and on the bytes
`plug update` is about to write over plug itself. A digest binds bytes to a claim;
a signature binds them to an author, and an author is what was missing.

plug trusts exactly one release key. If it ever has to be replaced, whether it
was lost or stolen, the replacement revokes the old one by the same act, and
every CLI already installed says so and asks to be reinstalled from the cluster.
Installing carries no signature check on purpose: it is aimed at a host you
typed, while you are watching, which is what fetching the core is not.

An agent too old to answer `sig=` is refused, with no grace period. That costs
nothing to anyone who has not moved: an older CLI never asks for a signature, so
an untouched CLI and agent pair keeps working exactly as it did. The only pair
this refuses is one where half has already been updated, and there the answer is
to update the other half, which for a developer tool is one command.

The signed statement covers the platform and the hash, and deliberately not the
version: an embedder that links the agent into its own binary announces its own
version while serving these binaries, and binding it would refuse every launch
there.

### The agent stopped announcing that it was ready over work it had not finished

Two failures inside the agent were being absorbed and never mentioned. A panic
during an SSH handshake released nothing, so the slot that connection held was
gone for good: sixty-four of them and the agent refused every new peer while
still logging that it was ready. And the boot sweep, which restores whatever a
previous session had parked, discarded its own panic one line before that same
ready message, so a deployment could sit at zero replicas with nothing anywhere
saying the sweep had not finished. Both now say what happened, and both still
refuse to take the process down with them.

### The command validator on the agent had no test at all

Everything arriving from the network reaches one function, which checks that a
name is a DNS label a cluster will accept and that a port is a port. Nothing
exercised it, because it answers by exiting. Widening the name rule to accept
anything at all left the whole suite green. It no longer does.


### The anonymous download account could be made to pick its own verb

The agent's `get` account takes its command from the caller and split it without
disabling globbing, so a command of `*` expanded against whatever files sat in
the working directory and the verb actually run depended on that. It is the only
unauthenticated entry point the agent has. It now splits without globbing.

### The e2e suite had three checks that could go green without measuring anything

The guard against the three e2e families drifting apart compared only a cell's
name, condition and script, so a family could mark a cell `continue-on-error`,
or run it under a different shell, and the guard still reported that all three
ran the same cells. The Kubernetes leg rewrote the published manifest to point
at the branch image with a `sed` whose result nobody checked, and `sed` succeeds
when it substitutes nothing: the day that manifest changes, the leg would have
tested a published image and gone green. And the lookup for the previous release
read only the first page of tags, which after enough pushes would have returned
the CURRENT release as the previous one, failing the update cells while blaming
the code.

Separately, the reaper that frees an orphaned cluster when its caller run ends
had stopped firing at all: the identifier it parses gained a field and the split
was never updated, so every cluster ran to its full timeout, holding runners for
about three hours per pipeline. Every service image the suite uses is now pinned,
as the rest of the fleet already was, and the Docker actions in the publishing
workflow are pinned to commits like the others.

## 2.12.1

### Windows: the binary a SYSTEM service runs is no longer yours to rewrite

plug installs under `%LOCALAPPDATA%\Programs\plug`, and the datapath service
runs that binary as SYSTEM. The directory was writable by you, so anything
running under your account could replace `plug.exe` and be SYSTEM the next time
the service started. It takes code already running as you, which is exactly what
a hostile dependency in the project you are developing is.

`plug install-service`, the one command that already runs elevated, now leaves
that directory writable by administrators and SYSTEM only. Nothing moves and the
install path is unchanged.

**What this costs you:** `plug update` on Windows now needs an elevated shell to
replace the binary, and says so when it hits the refusal instead of reporting a
bare access error.

### macOS: attributing a flow no longer re-climbs the process tree every time

This only ever mattered when two or more different cluster agents are live at
once on the same Mac. With one, the datapath routes straight through and none of
this runs.

With several, every new TCP connection was attributed by walking the connecting
process's ancestry, and each hop costs two forks. Measured on a real machine:
about 16 ms each, three hops deep, so roughly 80 ms per connection on top of the
80 ms it takes to find the process in the first place. A database pool opening
ten connections paid that walk ten times over for an answer that had not changed.

The walk is now remembered per process. What is deliberately NOT remembered is
the check that the process is still the same one: a recycled process id is
precisely what the walk refuses on, and a cached answer past it would send your
traffic into somebody else's cluster. So that stamp is re-read on every single
flow, one fork instead of five. Refusals are not cached either, since a launcher
may simply not have registered yet when its first flow lands.

Finding the process still costs its own fork per connection, and that one is not
a cache away: it changes with every socket. Removing it means reading the socket
table without forking, which needs cgo, and the datapath builds without it on
purpose.


---



### The end-to-end suite now checks the privilege it never checked

plug runs your command with a privilege you do not have: root on macOS, file
capabilities on Linux. It drops that privilege for your command and keeps it for
its own work, and that drop is the single most important property of the whole
arrangement.

It was asserted nowhere. The harness that exercises nineteen cells across three
orchestrators and three operating systems contained no `id -u` and no `whoami`.
The property was a comment.

Every leg now checks two things about the process plug starts for you: that it
runs under YOUR user id, which is what a setuid launcher leaks, and that on Linux
it carries no capabilities, which is what the ambient set leaks past an exec.
That second one was a real leak until this release, on any launch that did not
need a private resolver. A leg that happens to run as root reports the check as
not measurable rather than passing it, since a root child proves nothing there.


---



### Privileged helpers are found where root keeps them, not on your $PATH

plug drives system tools by name to set up its network device: `ip`, `sysctl`,
`ifconfig`, `route`, `scutil`. It hands them its own privilege, because file
capabilities do not survive an exec and the helper would otherwise run with
none. A name resolved through your `$PATH` therefore meant a `PATH=/tmp/evil:…`
holding a fake `ip` was a fake `ip` running with `CAP_SYS_ADMIN`, or as root on
macOS.

The launcher already narrowed `$PATH` for exactly this reason, and that guard
never fired on Linux: it returns early when the effective and real user match,
which is precisely what file capabilities give you. The one platform its own
comment named was the one it did not cover.

The lookup now happens where the command is run, against root-owned system
directories, and `$PATH` is never consulted. The directory list is the same one
the launcher narrows to, so NixOS and the mainstream layouts are both covered,
and a tool nobody can find fails saying where plug looked. An absolute path is
taken as given, for anyone who moved a tool on purpose.

Nothing changes for your own command: `$PATH` reaches it untouched, exactly as
before. This is only about the tools plug runs for you, with its own privilege.

### Fixed: a released image with a flavour was read as a development build

`2.12.0-hosted` is a published release, and one predicate did not think so. A
digest that did not match on such an image was re-fetched in silence instead of
saying that a published build naming one commit cannot legitimately change
bytes. The flavour was also dropped from version strings plug prints.

The neighbouring check stays deliberately flavour-blind: it picks the newest
release a cluster should move to, and a hosted tag is not a newer version of a
standalone one.


---



---

## 2.12.0

### The SSH server gets back what sshd gave the port for free

Replacing OpenSSH with a Go server removed a setuid binary, two Unix accounts
and nineteen untested lines of configuration. It also quietly dropped four
things sshd did without being asked, each one a way a single stranger could cost
the agent something no legitimate client ever asks for.

**A handshake that never finishes is dropped** after a minute, which is what
LoginGraceTime did. A peer that connected and said nothing held a goroutine, a
socket and a slot for as long as the agent lived. The deadline covers the
handshake only and is cleared the moment it succeeds, or a slow `install`
download would be cut halfway through.

**Handshakes in flight are capped**, the way MaxStartups counts them: only peers
that have not authenticated yet hold a slot, so an established session never
takes one and a busy team is never turned away. Past the cap a connection is
refused rather than queued, because a queue outlives the flood that created it.

**A panic costs its own connection and nothing else.** sshd answered for that
with a process per connection; here it is goroutines inside a gateway with other
work to do.

**And the server's keepalive can no longer wait forever.** It asked the peer a
question with no deadline of its own, so a client whose TCP was still up but
which answered nothing blocked there indefinitely: the miss counter never moved
and the connection kept its remote forwards, which means it kept its NAME, until
the agent restarted. The client side already had this timeout.


---



### Security: the channel that delivers the binary now records who answered

Four findings from the audit, none of them exotic, all of them the same shape:
an invariant the code states somewhere and does not apply everywhere.

**The download channel ignored the agent's host key.** It carries the version,
the digest that version must hash to, and the binary itself, which then runs as
root on macOS and with ambient CAP_SYS_ADMIN on Linux. It now records the key on
first use and says so when it changes, exactly like the data tunnel, on the same
file. Detection rather than prevention, because an agent legitimately gets a new
key when its container is recreated and blocking would fail every session after
a routine redeploy. A comment nearby had been claiming this was already the case.

**`plug update` fetched the digest and never compared it.** It used it to decide
whether replacing was necessary, then wrote the downloaded bytes over a setuid
root binary having checked their size and that they looked like an executable.
The core has always been verified this way; the launcher is the more privileged
of the two.

**The store guard covered the write but not the read.** On macOS it is what
stands in for the TOCTOU defence Linux gets from /proc/self/fd, and a cache hit,
which is the common case and the one that ends in running that file, went
through unchecked.

**On Linux, a session could hand your command plug's capabilities.** The ambient
set is cleared by the mount-namespace shim, and any launch that does not need a
private resolv.conf skips that shim: an unwritable TMPDIR was enough to run your
`npm run dev` with CAP_SYS_ADMIN. It is now cleared on that path too.

Two CI changes ride along: the workflow token is read-only by default, and the
job that kills the compose clusters now waits for the arm64 leg, which uses them.

### Dependencies

`golang.org/x/crypto` moves from v0.53.0 to v0.55.0 in both modules. It is the
SSH implementation on both sides, the client's tunnel and the agent's server,
so it is the one dependency here worth keeping current on its own account.
`golang.org/x/sys` follows to v0.47.0.


---



### Two images, two clients: the commands follow the cluster that served them

plug is distributed BY the cluster it serves: `ssh get@<agent> install | sh`
hands out the binaries baked into that agent's image. So a gateway that embeds
the agent ships its own client, and some commands make no sense against it.

There are now two images. `softwarity/plug` is the standalone product, unchanged.
`softwarity/plug:hosted` carries the client for a gateway that embeds the agent:
it has `keygen` and `pubkey`, and it has no `update` and no `versions`, because
the gateway serves the client and the list that counts is the gateway's.

A command that does not apply is **absent**: missing from `plug --help` and
refused if typed, in both spellings (`plug update -p X` and `plug -p X update`).
Not hidden behind a flag, since a flag has to be known in advance and is
discovered by the person who did not know it existed.

The flavour rides in the version string (`2.12.0-hosted`) and nowhere else.
That string is already the cache key for the core a launcher fetches and the
value the agent announces, so two flavours cannot collide on one cached binary
and nothing can drift out of sync with anything.

**The trade, stated plainly:** the command set follows where the binary came
from, not which cluster it is talking to. There is one plug in a PATH. Someone
who installed from a gateway and later reaches a standalone cluster still has no
`update`. Deciding per call instead would keep every command everywhere; this is
the deliberate other choice.

### /opt/plug now holds only what the agent hands out

`authorized_keys` and `state/` moved to `/var/lib/plug/`. They are the standalone
agent's own admission list and its host key, and `/opt/plug` is what the agent
DISTRIBUTES: the client binaries, the version, the installer, the WinTUN DLL.
An embedder copies that directory wholesale, and keys travelling there read as
meaningful when they are not, since a gateway takes its identity from its vault
and its admissions from its database.

Nothing in the published manifests mounts either path, so this changes nothing
for a normal deployment. If you mounted a volume over `/opt/plug/state` to keep
a host key across restarts, point it at `/var/lib/plug/state`.


---



### Fixed: a profile's key was never actually offered

If you generated a key with `plug keygen`, enrolled the public half exactly as
`plug pubkey` printed it, and were still refused: the key was never presented.
plug offered the shared key built into the binary instead, so the agent refused
a fingerprint that appeared nowhere in what you had just done.

Both halves were right on their own, which is what made it so hard to see. The
LAUNCHER resolves your profile and knows the key. The CORE is a separate process
and is what opens the tunnel. Everything crossing between them travels in a
handful of environment variables, and the key was not one of them. On macOS
there is a third process, the datapath daemon, which holds one tunnel per
cluster and knows a cluster only as a host and a port; it dialled with no
personal key at all. Both boundaries now carry it, and a test presents the
profile's key to a real SSH server and compares what arrives with what `pubkey`
prints, on both sides of the exec.

The failure told you nothing useful either, and that is fixed too.

**The refusal now names the file, not just a fingerprint.** The agent can only
say `SHA256:… is not authorized`, because it has no idea where that key came
from. plug does. Every key it offered is now listed with the file it was read
from, and a line saying that what to enrol is what `plug pubkey` prints.

**A refused key is no longer retried three times a second.** The macOS and
Windows daemons reconcile every 300 ms, and an authentication failure was
treated like a network blip, so a stated reason became a stream of handshakes.
What you saw instead was a session that merely felt slow and then failed
somewhere else entirely, usually on the first thing your command tried to reach.
A refusal is now recognised for what it is, reported, and left alone for a
minute before being tried again. A cluster that is simply unreachable is still
retried immediately, since that one does fix itself.

One hardening rode along: the key path arrives through the environment and is
read by a process holding privilege, so it now goes through the same guard as
every other privileged path under your home. It reads only where you could have
read it yourself.

There was a second half to this, and it is the one that would have kept biting
after the first fix. **The core that opens your tunnel is the CLUSTER's version,
not your launcher's.** So a cluster whose agent predates per-profile keys forces
a core that predates them too, and that core ignores the key however new your
launcher is. The symptom was the same refusal, plus a detail that made it look
impossible: `plug test` authenticated and the host named the developer, because
`plug test` never leaves the launcher, while every tunnel was refused with the
shared key's fingerprint. One machine, one profile, two identities.

plug now declines that handoff. When a profile has a key and the cluster's core
would drop it, plug runs its own core instead and says so, because losing the
version match costs less than losing the identity. Upgrading the agent lines
them back up.

The invariant is pinned three ways: what the `test` path presents, what the
tunnel presents, and what a core rebuilt from the environment presents must be
the same list of keys, in the same order, for one profile.


---



### A profile can carry your own key

Until now plug authenticated with one key built into the binary. It is in every
published build, so it proves the caller has plug, not who the caller is. That is
the honest description of a trusted development cluster and it does not change
here: nothing you run today needs a key of its own.

What is new is that a profile can have one.

    plug keygen -p dev      generate this profile's pair, kept in ~/.plug/keys
    plug pubkey -p dev      print the public half, to hand to whoever runs the cluster

One pair per profile, because an identity per cluster is the unit an operator
enrols and revokes: a single key shared across clusters could not be withdrawn
from one of them without breaking the others.

**It is safe to run before the cluster asks for it.** plug offers both keys, the
profile's own first and the built-in one behind it, and SSH lets the agent pick
whichever it knows. An agent that does not check keys accepts the built-in one
and never sees the other; an agent that does accepts yours. There is no order to
get right and no flag day, which is the whole reason the fallback is there.

`plug test` now says which identity the agent recognised, so an enrolment can be
verified from the outside rather than assumed. The pair follows its profile: it
moves on `plug rn` and it goes on `plug rm`, so a private key is never left on
disk with nothing pointing at it.

If you never run `plug keygen`, nothing about your setup changes.


---


### The agent reports what is being served

For anyone linking the agent into their own gateway. The `Host` interface has
always declared `Served` and `Unserved`, and nothing ever called them: an
embedder could implement both, run, and watch an empty state page forever.

They fire now. A name being served reports who is serving it, on which cluster
ports, and since when. It is withdrawn when the session releases it and, more
importantly, when the session simply dies: a laptop that sleeps, a process
killed, a cable pulled. The event also says whether a deployed workload had to be
parked to make room, so a state page can tell "somebody is serving this name"
from "somebody is serving this name and your deployment is stopped while they
do". Three cases the obvious implementation gets wrong are handled and tested: a
refused serve announces nothing even though it exits zero, a failed release
withdraws nothing, and a name already granted to a newer session is not withdrawn
when its former holder finally tears down.

Delivery is on one goroutine, so the callbacks keep their order and a gateway
that is slow, broken or panicking costs its own events and never a developer's
session. That is what the interface promised; it is now what it does.

Four things a gateway had no way to say are now fields on `Config`, each
defaulting to exactly what a standalone agent does today.

`Version` is what the agent reports. It defaults to `/opt/plug/VERSION`, a file
that exists in the plug image and nowhere else, and answering "unknown" is not
cosmetic: the launcher turns that answer into a cache path and refuses to run a
core whose digest the agent cannot vouch for. `SignpostImage` is the image the
signpost container runs on Compose and Swarm; it defaulted to the agent's own
image, which is correct when the agent is the plug image and produces a
container that dies instantly when it is not. Kubernetes was never affected
there, since it points a Service at the agent and creates no pod.

`NoSelfUpdate` refuses the verb that rewrites the image of the deployment the
agent runs in. Standalone that deployment is plug, which is the point; inside a
gateway it is the gateway. And `NoDownloadAccount` removes the anonymous `get`
account, which has no authentication by design and is bounded only by its fixed
command: a reasonable surface on a dedicated agent, a decision worth making
deliberately on a gateway's own port.

Two of these fail silently when left wrong, so the agent now says so at boot
rather than letting someone discover it on their first `plug -s`.


---



### The agent speaks SSH without carrying OpenSSH

Nothing changes for you. Same commands, same protocol, same CLI; a client from
an earlier version talks to this agent, and this CLI talks to an older agent.
Nobody has to update anything in step.

What changes is inside the container. The agent used to be OpenSSH plus a helper
bolted onto it, and that shape had a price paid in three places. The helper was
**setuid root**, for one reason only: sshd ran it as an unprivileged user while
it needed the Docker socket or the pod's service-account token. Nineteen lines of
sshd configuration were generated by `echo` at build time, so no test ever read
them. And installing the SSH server meant reaching the network during the build,
which is how one TLS error on a package mirror once cost three test legs and kept
a release from shipping.

The agent is now one static Go binary that speaks SSH itself. The setuid bit is
gone, there are no Unix accounts left in the image, nothing is installed at build
time, and what the server does is written down in one file instead of being
spread across a Dockerfile: two accounts, public-key for the tunnel and none for
downloads, one fixed command each with no shell behind it, and the two forwarding
primitives.

Said plainly, because it cuts both ways: the transport used to be third-party
software with a long track record, and it is now ours. In exchange, the surface
is a fraction of what it was - a library rather than a full server, of which
plug used three features out of hundreds - with no shell and no privileged
helper left in the container. The full end-to-end suite exercises it: eight
protocols in four languages, three orchestrators, three operating systems, over
a real VPN.


---


### The agent is a package, not only a binary

Nothing changes if you deploy the agent as the manifests describe: same image,
same commands, same behaviour. This one is for a different reader.

The agent's code is now importable, so another Go program can link it in and
serve plug sessions itself instead of running a second container beside it.
What that program has to supply is small and named: the server's identity,
which client keys are accepted and under whose name, and what is being served
at any moment. Everything else comes with the package.

Standalone plug uses that same interface, with the keys baked into the image
and an identity kept on disk. It is not a special case kept alive for tests: it
is the same code path, so the embedded mode cannot quietly drift away from the
one everybody runs.

One behaviour differs, and only for an embedder. An agent that cannot reach its
orchestrator refuses to start, which is right for a container whose whole job is
that: a healthy-looking agent failing on someone's first `-s` would hide a
missing mount. A program that embeds the agent gets an error instead and decides
for itself, because a gateway should not fail to boot over a permission its
users may never need.

The import path is `github.com/softwarity/plug/agent`.


---

### A Kubernetes name plug reclaimed could point at no pod at all

When plug takes back a Service it created itself - one left warm by a linger, or
orphaned by a crash - it repoints it at the agent by writing `{app: plug}` into
the selector. A merge patch merges maps key by key: on a Service whose selector
carried anything else, that key was ADDED to the originals instead of replacing
them. The selector then demanded `app=plug` *and* the original workload's labels,
which no pod satisfies. The Service ended up with zero endpoints, every
connection to the name timed out, and ninety seconds later the session reported
the name "not reachable inside the cluster" - pointing at the cluster's
scheduler, which had nothing to do with it.

Taking over someone else's workload always did the right thing: it nulls the
extra keys explicitly. The two paths now build the same patch in the same place,
so they cannot drift apart again.


---

### Two shapes of Service are refused up front instead of timing out

A headless Service (`clusterIP: None`) has no virtual IP - the name resolves
straight to pod IPs and `targetPort` is never applied. An `ExternalName` is a DNS
alias carrying no endpoints and no ports. Repointing either one SUCCEEDS, since
the patch itself is valid, and yields a name that answers nobody.

That used to surface ninety seconds later as a timeout, with the deployed
workload parked the whole time. The agent now refuses while it still holds the
object, naming the shape and the way out. `clusterIP` is immutable, so there is
no in-place fix to suggest: the Service goes, or the name does.


---

### A served name points at the agent holding the session, not at "an agent"

Nothing changes if you run one agent, which is what the manifests deploy and what
plug is. **Re-apply `deploy/plug-k8s.yaml` if you use Kubernetes**: it grants one
more thing (the endpoints of the Services plug itself creates) and passes the pod
its own address. An agent whose deployed RBAC predates that keeps working exactly
as before, and says so once in its log rather than failing anyone's session.

A name plug creates has to reach the process serving it, and that process lives in
exactly ONE agent: the session's forward is a socket on one machine. Two of the
three backends named the agent's *role* instead. On Swarm the signpost relayed to
the service VIP, which load balances across every task; on Kubernetes the Service
selected `app: plug`, which matches every replica. With a single agent those are
the same thing, which is why it worked, and why `-s` simply refused to run at more
than one replica on Swarm. Past one they become a lottery: a share of the requests
lands on an agent that never heard of the session.

So the Swarm signpost now relays to the *task* holding the session
(`<service>.<slot>.<id>`, which the overlay's DNS answers with that one task), and
the Kubernetes Service carries no selector at all - the agent writes its endpoints
itself, one address, the pod that is serving. The refusal at more than one replica
is gone with the reason for it. Note what this does NOT need: no access to pods.
Labelling its own pod and selecting that would have meant the right to rewrite
pods in the namespace, which is a much larger grant than writing the endpoints of
names plug created.

### A parked workload comes back even if the agent that parked it never does

When plug takes a deployed service's name for a session, it writes down what it
set aside so it can put it back. That receipt named what was parked, never by
whom, so exactly one agent could act on it: the one that wrote it, when it next
started. A dedicated agent container always comes back under its own name and
finds its own leftovers, so this was invisible.

It stops being invisible where the agent is a package inside something that
scales. The instance that parked a workload may never come back - scaled down,
rescheduled, replaced - and then nobody restores it: the real service stays at
zero replicas, or the name stays pointing at a machine that has gone home, with a
receipt no one feels entitled to act on.

Receipts now name their owner, as the address that proves it: the instance, and
the port the session answers on. Any agent may restore one whose owner no longer
answers, and none may touch one whose owner does. Nothing is renewed and nothing
is coordinated - a forward dies with its process, which is the whole signal. The
same evidence settles the mirror-image bug it exposed: a booting agent used to
restore *every* parked workload it found, including ones a colleague was serving
from another instance at that moment.

Standalone plug gains from it too: an agent redeployed under a different name can
restore what its predecessor parked, which it could not before.


---


## 2.11.1

### Kubernetes clusters answer "that name does not exist" in time again

To tell you a name is absent rather than guess, the agent proves its cluster's
resolver is alive at all — otherwise "I cannot find it" and "my DNS is broken"
are the same sentence. That proof is not the same work everywhere: on Docker and
Swarm it is this agent's own name, answered from memory in a millisecond or two;
on Kubernetes it goes to CoreDNS, a pod, across the network. The time allowed was
sized for the first.

It also ran *after* the lookup rather than beside it, so a Kubernetes cluster
waited for both in turn. Past the client's patience, plug stops waiting and mints
an address rather than lie — which is correct, and which meant an absent name
could answer instead of failing cleanly.

The two now run side by side, and the proof has twice the room. The worst case
did not grow.


---

### `plug doctor` now checks the resolver your programs use, not only its own

It could report a clean bill of health on a machine where nothing resolved at
all. Every local check asked plug's own resolver — which answered in
milliseconds, because it was fine — while `getaddrinfo`, the path every program
actually takes, failed after thirty seconds. doctor printed "no problems", then
offered its one remedy for a slow lookup, which points at Docker Desktop and had
nothing to do with it.

There is now a check that resolves a dotted name the way a program resolves it.
When that fails or drags, the remedy names the **system** resolver — flushing
mDNSResponder, `resolvectl`, the DNS Client service — and never a container
runtime that is not involved.

It also reports cores left where the store used to live before 2.11.0. `plug
prune` clears them; nothing else would have mentioned they were there.

---

## 2.11.0

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
