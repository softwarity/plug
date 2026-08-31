# Releasing plug

Notes for whoever cuts a release. The user-facing documentation is the
[site](https://softwarity.github.io/plug/); this file is the part that only a
maintainer needs, and that would otherwise live in somebody's memory.

## The release signing key

plug runs the core with the privilege it holds: root on macOS, `CAP_SYS_ADMIN`
on Linux. The core is fetched from the agent, and which agent is asked is chosen
by whoever launches plug (`-H`, or a `host =` line in a profile the user owns).
So the bytes that get executed as root come from a party the attacker can pick.

A digest cannot close that, because the same party announces it. Only a signature
made with a key that party cannot reach does. That key is the release key.

### Where it lives

| Where | What | Why |
|---|---|---|
| `cli/keys/release_ed25519.pub` | public half, committed | compiled into every plug binary with `go:embed`; this is the trust anchor, and it has to be somewhere the attacker cannot write |
| GitHub secret `PLUG_RELEASE_KEY` on `softwarity/plug` | private half, base64 | the only place CI reads it. `_docker.yml` passes it to the image build as a BuildKit **secret mount**, never a build-arg: a build-arg is recorded in the image history and readable by anyone who pulls |
| Your own vault, outside GitHub | private half | see below, this one is not optional |

**A GitHub secret is write-only.** Once set it can never be read back, by you or
by anyone. If the secret is your only copy, you have no copy. Keep the private
half in a password manager or an offline vault as well.

### Setting it

```sh
gh secret set PLUG_RELEASE_KEY --repo softwarity/plug < release_ed25519.key
```

The file is base64 of the 64-byte ed25519 private key, one line.

### What a build does with it

`agent/Dockerfile` mounts it for the length of one `RUN` and calls
`cli/cmd/plug-sign`, which writes a `.sig` next to each `plug-<os>-<arch>`
binary. `agent/serve-binary` then answers `sig=` alongside `sha256=` on the
`digest` verb, and the launcher verifies it before executing the core, on a
cache hit as well as on a download, and before `plug update` overwrites plug
itself.

A build with no key mounted signs nothing and says so: that is the normal case
for a local `docker build` and for a fork, and the launcher tolerates an unsigned
core until the cutover date in `cli/release_sig.go`. A release build that takes
that path silently would publish an image that stops working on the cutover date,
so the message it prints is worth grepping for if a release ever looks wrong.

### If it is lost or stolen: replace it

**One key at a time.** There is no spare, and that is a decision rather than an
omission. A list of trusted keys makes recovery from a lost key cheaper, and
makes recovery from a stolen one worse: the stolen key keeps working until
somebody deliberately deletes its line. With a single key, replacing it and
revoking it are the same act, and there is no second step to forget.

The procedure is the same either way:

1. Generate a new pair.
2. Put the public half in `cli/keys/release_ed25519.pub`, replacing the old one.
3. `gh secret set PLUG_RELEASE_KEY` with the new private half.
4. Release.

**Every CLI already installed stops working at that point**, immediately, not on
the cutover date: the date only tolerates a signature that is *absent*, never one
that fails to verify. Those CLIs print the reinstall command and their users run
it. That is the accepted cost, and it is why the failure message is worth keeping
accurate.

This works because `install` carries no signature check, deliberately. Installing
is an act aimed at a host the user typed, over an SSH host key they accepted,
while they are watching. Fetching the core is the opposite: automatic, invisible,
at every launch, from an address read out of a profile file that any code running
as the user can rewrite. The signature exists for the second one. Bolting it onto
the first would only remove the escape hatch that makes replacing the key
survivable.

### The cutover date

`signedFrom` in `cli/release_sig.go`. Before it, an unsigned core is accepted
with a warning. After it, refused.

It is a date and not a version on purpose: a fake agent announces its own
version, so any rule that read the version would be told whatever made it pass.
A date is the one input the party being checked does not control.

## The other secrets this repo uses

- `DOCKERHUB_USERNAME` / `DOCKERHUB_RW`: the account images are pushed as.
  Declared explicitly by `_docker.yml` and passed by name, never inherited.
- `PAT_TOKEN`: `contents: write` on main, used by the release flow to push the
  version commit and tag. Deliberately out of reach of the image jobs.
