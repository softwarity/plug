package main

// A profile's PERSONAL key pair: plug generates it, keeps it, and offers it to
// that one cluster's agent alongside the key built into the binary.
//
// Why a verb and not `ssh-keygen`: the install script lands the binary BEFORE
// there is any key to speak of, through `| sh`, on machines that need not carry
// OpenSSH at all (Windows, slim images). A verb has no such dependency, owns the
// format it will later have to read back, and gives rotation a place to live.
//
// Why one pair per profile: an identity per cluster is the unit an operator
// enrols and revokes. A single key shared across every cluster could not be
// withdrawn from one of them without breaking the others.
//
// Why this is safe to run before the cluster asks for it: plug offers BOTH keys
// (see config.authKeys). An agent that does not check keys accepts the built-in
// one and never sees the personal key; an agent that does accepts the personal
// one. There is no flag day, and no order to get right.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
	"golang.org/x/crypto/ssh"
)

// keysDir is where the pairs live: ~/.plug/keys, beside the profiles naming them.
func keysDir() string { return filepath.Join(plugDir(), "keys") }

// profileKeyPath turns a profile name into its private-key path (the public half
// is the same name with .pub). It validates the name first, exactly as
// profilePath does and for the same reason: the name becomes a FILE NAME, plug
// may hold root while it writes there, and filepath.Join RESOLVES a leading
// "../.." instead of refusing it.
func profileKeyPath(name string) string {
	if err := checkProfileName(name); err != nil {
		fatal("%v", err)
	}
	return filepath.Join(keysDir(), name)
}

// pickProfileName resolves WHICH profile a key verb acts on. Unlike
// resolveConfig it never runs the wizard: keygen on a cluster you have not
// described yet is a mistake, not an invitation.
func pickProfileName(explicit string) string {
	if explicit != "" {
		if _, err := os.Stat(profilePath(explicit)); err != nil {
			fatal("no profile %q in %s, create one first with 'plug init'", explicit, plugDir())
		}
		return explicit
	}
	names := listProfiles()
	switch len(names) {
	case 0:
		fatal("no profile in %s, create one first with 'plug init'", plugDir())
		return ""
	case 1:
		return names[0]
	default:
		return chooseProfile(names)
	}
}

// parseKeyArgs reads the flags the two key verbs share. Hand-parsed rather than
// routed through parseArgs, which serves the run path and knows -s/-c.
func parseKeyArgs(verb string, args []string) (profile string, renew bool) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			if i+1 >= len(args) {
				fatal("%s: -p wants a profile name", verb)
			}
			i++
			profile = args[i]
		case "--renew":
			renew = true
		case "-h", "--help":
			fmt.Print(usage())
			os.Exit(0)
		default:
			fatal("%s: unknown argument %q", verb, args[i])
		}
	}
	if renew && verb != "keygen" {
		fatal("%s: --renew belongs to 'plug keygen'", verb)
	}
	return profile, renew
}

// cmdKeygen implements `plug keygen [-p profile] [--renew]`.
func cmdKeygen(args []string) {
	profile, renew := parseKeyArgs("keygen", args)
	name := pickProfileName(profile)
	priv, pub := profileKeyPath(name), profileKeyPath(name)+".pub"

	// Refusing to overwrite is the whole point of --renew. A silent regeneration
	// would revoke an enrolment the operator has already accepted, and the only
	// symptom would be a refused connection with a key that "has not changed".
	if _, err := os.Stat(priv); err == nil && !renew {
		fatal("profile %q already has a key (%s).\n"+
			"      'plug pubkey -p %s' prints it; 'plug keygen -p %s --renew' replaces it,\n"+
			"      which means enrolling the new public half before the old one is withdrawn",
			name, priv, name, name)
	}

	writeKeyPair(name, priv, pub)
	setProfileKey(name, "key", priv)

	line, err := os.ReadFile(pub)
	if err != nil {
		fatal("cannot read back %s: %v", pub, err)
	}
	parsed, _, _, _, perr := ssh.ParseAuthorizedKey(line)
	if perr != nil {
		fatal("the key just written does not parse: %v", perr)
	}
	if renew {
		info("profile %q: key replaced (%s)", name, ssh.FingerprintSHA256(parsed))
	} else {
		info("profile %q: key created (%s)", name, ssh.FingerprintSHA256(parsed))
	}
	info("enrol this public half with whoever operates the cluster:")
	fmt.Print(string(line))
	info("plug keeps offering its built-in key too, so this profile keeps working " +
		"whether or not the agent checks keys")
}

// cmdPubkey implements `plug pubkey [-p profile]`: the public half on stdout and
// nothing else, so `plug pubkey | pbcopy` carries exactly what gets enrolled.
func cmdPubkey(args []string) {
	profile, _ := parseKeyArgs("pubkey", args)
	name := pickProfileName(profile)
	pub := profileKeyPath(name) + ".pub"
	line, err := os.ReadFile(pub)
	if err != nil {
		fatal("profile %q has no key yet, create one with 'plug keygen -p %s'", name, name)
	}
	fmt.Print(string(line))
}

// writeKeyPair generates an ed25519 pair and puts it on disk under the user's
// own ownership, whatever privilege plug happens to be holding.
//
// The private half is written to a temporary file and renamed into place: a
// rotation that died halfway would otherwise leave the profile pointing at a
// truncated key, and the failure would read as "the agent refuses my key".
func writeKeyPair(name, priv, pub string) {
	guardUserPath(priv) // plug may hold root here, never write outside the caller's tree
	guardUserPath(pub)
	if err := os.MkdirAll(keysDir(), 0o700); err != nil {
		fatal("cannot create %s: %v", keysDir(), err)
	}
	chownToUser(keysDir())

	_, secret, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal("generating a key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(secret, keyComment(name))
	if err != nil {
		fatal("encoding the private key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(secret)
	if err != nil {
		fatal("deriving the public key: %v", err)
	}
	authorized := append(
		[]byte(strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(signer.PublicKey())), "\n")),
		[]byte(" "+keyComment(name)+"\n")...)

	tmp, err := os.CreateTemp(keysDir(), ".plug-key-*")
	if err != nil {
		fatal("cannot write in %s: %v", keysDir(), err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		fatal("cannot restrict %s: %v", tmp.Name(), err)
	}
	if _, err := tmp.Write(pem.EncodeToMemory(block)); err != nil {
		tmp.Close()
		fatal("cannot write %s: %v", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		fatal("cannot write %s: %v", tmp.Name(), err)
	}
	chownToUser(tmp.Name())
	if err := os.Rename(tmp.Name(), priv); err != nil {
		fatal("cannot install %s: %v", priv, err)
	}
	chownToUser(priv)

	if err := os.WriteFile(pub, authorized, 0o644); err != nil {
		fatal("cannot write %s: %v", pub, err)
	}
	chownToUser(pub)
}

// keyComment is what an operator reads in a list of enrolled keys, so it names
// the two things they need to tell one entry from another: which profile, and
// whose machine.
func keyComment(profile string) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return "plug-" + profile + "@" + host
}

// removeProfileKeys deletes a profile's pair. Called from `plug rm`, so a
// removed profile does not leave a private key lying around under ~/.plug.
func removeProfileKeys(name string) {
	for _, p := range []string{profileKeyPath(name), profileKeyPath(name) + ".pub"} {
		guardUserPath(p)
		if err := os.Remove(p); err == nil {
			info("removed its key %s", p)
		}
	}
}

// renameProfileKeys follows a `plug rn`: the pair moves with the profile and the
// profile's `key =` line is repointed. Without this the renamed profile would
// name a path that no longer exists, and authKeys is fatal on that.
func renameProfileKeys(old, name string) {
	oldPriv, newPriv := profileKeyPath(old), profileKeyPath(name)
	if _, err := os.Stat(oldPriv); err != nil {
		return // no key to move
	}
	for _, s := range []struct{ from, to string }{
		{oldPriv, newPriv},
		{oldPriv + ".pub", newPriv + ".pub"},
	} {
		guardUserPath(s.from)
		guardUserPath(s.to)
		if err := os.Rename(s.from, s.to); err != nil && !os.IsNotExist(err) {
			info("could not move %s to %s: %v", s.from, s.to, err)
		}
	}
	setProfileKey(name, "key", newPriv)
}

// reportIdentity says which identity the agent recognised, which is the only way
// to find out that an enrolled key actually took.
//
// It runs over the TUNNEL user, not the download user `plug test` used a line
// earlier: the download account authenticates with nothing, so it could never
// answer the question. Best effort throughout. This is a nicety on top of a
// reachability check, and an agent too old to answer `info`, or one that does
// not identify anyone, must not turn a successful test into a failure.
func reportIdentity(cfg config) {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, cfg.authKeys(), tun.SharedKnownHosts(), nil)
	if err != nil {
		if cfg.key != "" {
			info("  note: the agent refused the tunnel key (%v).\n"+
				"        if you just ran 'plug keygen', the public half may not be enrolled yet", err)
		}
		return
	}
	defer tr.Close()
	out, err := tr.Exec("info")
	if err != nil {
		return
	}
	for _, f := range strings.Fields(out) {
		if w, ok := strings.CutPrefix(f, "who="); ok && w != "" {
			info("  it knows you as %q", w)
			return
		}
	}
	if cfg.key != "" {
		info("  it does not identify you: this agent accepts the key built into plug,\n" +
			"        so the profile's own key is offered but never needed")
	}
}
