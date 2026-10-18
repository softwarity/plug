package main

// plug against an agent that admits its members by approval (PLUG_ADMISSION=
// invite, see the agent's admission.go).
//
// Nothing changes for the developer beyond reading one message: the install
// line is the same, plug makes the machine a key of its own the first time the
// agent asks for one, presents it, and says what to ask of whom. A member then
// runs `plug accept <key>` or picks the request in `plug requests`, and
// `plug members` is where a member is revoked (`plug revoke <key>` in a script). All three ride the tunnel, so
// only a member can run them; anyone else learns the members' names from the
// refusal, which is enough to know whom to ask.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// adoptOwnKey gives the profile a key of its own after an agent that admits by
// approval refused the one built into plug, and reports whether it did. The
// key is the one `plug keygen` makes, in the same place: one shape, whichever
// way it got there. A process that does not know its profile (the shared
// datapath daemon) cannot, and the refusal it reports says what to run.
func adoptOwnKey(cfg *config) bool {
	if cfg.profile == "" || cfg.key != "" {
		return false
	}
	if checkProfileName(cfg.profile) != nil {
		return false
	}
	priv := profileKeyPath(cfg.profile)
	if _, err := os.Stat(priv); err != nil {
		writeKeyPair(cfg.profile, priv, priv+".pub")
	}
	setProfileKey(cfg.profile, "key", priv)
	cfg.key = priv
	info("%s admits its members by approval: this machine now has a key of its own for it (%s)", cfg.profile, priv)
	return true
}

// needsOwnKey reports the refusal adoptOwnKey answers.
func needsOwnKey(err error) bool {
	var af *tunnel.AuthFailure
	return errors.As(err, &af) && af.NeedsOwnKey()
}

// dialAgent is tunnel.Dial for the commands that run in the launcher and talk
// to the agent once: the profile's keys, the agent's request for a key of its
// own answered, and the refusal of an agent that admits by approval said in
// words a person can act on.
func dialAgent(cfg *config) (*tunnel.Transport, error) {
	dial := func() (*tunnel.Transport, error) {
		keys, err := cfg.authKeysErr()
		if err != nil {
			return nil, err
		}
		return tunnel.Dial(cfg.host, cfg.port, sshUser, keys, tun.SharedKnownHosts(), nil)
	}
	tr, err := dial()
	if needsOwnKey(err) && adoptOwnKey(cfg) {
		tr, err = dial()
	}
	if err != nil {
		return nil, explainRefusal(*cfg, err)
	}
	welcome(*cfg, tr)
	return tr, nil
}

// welcome says it when this connection made the machine the first member.
func welcome(cfg config, tr *tunnel.Transport) {
	if tr != nil && tr.Welcomed() {
		info("you are the first member of %s: from now on it admits only the keys a member accepts.\n"+
			"      A newcomer's install tells them what to ask; '%s requests' shows who is waiting",
			clusterName(cfg), plugFor(cfg))
	}
}

// clusterName is how a message names the cluster: its profile, else its address.
func clusterName(cfg config) string {
	if cfg.profile != "" {
		return cfg.profile
	}
	return cfg.host + ":" + cfg.port
}

// plugFor is the start of a command line that reaches this cluster again.
func plugFor(cfg config) string {
	if cfg.profile != "" {
		return "plug -p " + cfg.profile
	}
	return "plug -H " + cfg.host + " --port " + cfg.port
}

// admissionRefusal is the message for a refusal by an agent that admits by
// approval, "" for any other refusal.
func admissionRefusal(cfg config, af *tunnel.AuthFailure) string {
	if af.NeedsOwnKey() {
		return fmt.Sprintf("%s admits its members by approval, and this machine has no key of its own for it yet.\n"+
			"      Run '%s requests' once: plug makes the key and asks to join", clusterName(cfg), plugFor(cfg))
	}
	if af.Unavailable() {
		return fmt.Sprintf("%s admits its members by approval, and could not read or write them just now: "+
			"nothing was recorded. Try again in a moment; if it lasts, its members' store is what to look at", clusterName(cfg))
	}
	short, members, ok := af.Pending()
	if !ok {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s admits its members by approval. Your request is registered.\n", clusterName(cfg))
	b.WriteString("      Ask a member to run:\n")
	fmt.Fprintf(&b, "        %s accept %s\n", plugFor(cfg), short)
	fmt.Fprintf(&b, "      or to pick you in: %s requests", plugFor(cfg))
	if len(members) > 0 {
		fmt.Fprintf(&b, "\n      Members: %s", strings.Join(members, ", "))
	}
	return b.String()
}

// ---- the three commands ----

// admissionTarget is the cluster an admission command acts on: -p, or the only
// profile, or the one picked.
func admissionTarget(verb string, args []string) (config, []string) {
	var profile string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--profile":
			if i+1 >= len(args) {
				fatal("%s: -p wants a profile name", verb)
			}
			i++
			profile = args[i]
		case "-h", "--help":
			fmt.Print(usage())
			os.Exit(0)
		default:
			if strings.HasPrefix(args[i], "-") {
				fatal("%s: unknown argument %q", verb, args[i])
			}
			rest = append(rest, args[i])
		}
	}
	name := pickProfileName(profile)
	cfg, err := readProfileSoft(name)
	if err != nil {
		fatal("profile %q: %v", name, err)
	}
	return cfg, rest
}

// askAgent runs one admission verb and returns its answer, ending the command
// on a refusal: the agent says why ("error: ...") when it keeps no roster.
func askAgent(cfg *config, verb string) string {
	tr, err := dialAgent(cfg)
	if err != nil {
		fatal("%v", err)
	}
	defer tr.Close()
	out, err := tr.ExecAll(verb)
	if err != nil {
		fatal("asking the agent: %v", err)
	}
	if msg, refused := strings.CutPrefix(out, "error: "); refused {
		if strings.HasPrefix(msg, "unknown command") {
			fatal("the agent of %s predates admission by approval: it admits anyone who reaches its port", clusterName(*cfg))
		}
		fatal("%s", msg)
	}
	return out
}

type admissionRow struct {
	fp, name, by, from string
	since, seen, last  time.Time
}

// parseRows reads the agent's tab-separated lines of one kind.
func parseRows(out, kind string) []admissionRow {
	var rows []admissionRow
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) < 5 || f[0] != kind {
			continue
		}
		t := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
		r := admissionRow{fp: f[1], name: f[2]}
		switch kind {
		case "member":
			if len(f) >= 6 {
				r.by, r.since, r.seen = f[3], t(f[4]), t(f[5])
			}
		case "request":
			r.from, r.last = f[3], t(f[4])
		}
		rows = append(rows, r)
	}
	return rows
}

// ago is a duration as a list shows it.
func ago(t time.Time, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < 2*time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}

func shortKey(fp string) string {
	s := strings.TrimPrefix(fp, "SHA256:")
	if len(s) > 12 {
		s = s[:12]
	}
	return s
}

// choose asks for numbers among n, "all" when allowAll. Nil when the person
// cancels, or when there is no terminal to ask on: the list was shown, and a
// script gets the list and nothing else.
func choose(question string, n int, allowAll bool) []int {
	tty, err := openTerminal()
	if err != nil {
		return nil
	}
	defer tty.Close()
	in := bufio.NewReader(tty)
	for {
		fmt.Fprint(os.Stderr, question)
		line, err := in.ReadString('\n')
		if err != nil {
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}
		if allowAll && line == "all" {
			all := make([]int, n)
			for i := range all {
				all[i] = i
			}
			return all
		}
		picked, ok := parseChoice(line, n)
		if ok {
			return picked
		}
		fmt.Fprintf(os.Stderr, "  numbers between 1 and %d, separated by spaces\n", n)
	}
}

// parseChoice reads "1 3" or "1,3" into indexes, refusing anything out of range.
func parseChoice(line string, n int) ([]int, bool) {
	var out []int
	seen := map[int]bool{}
	for _, w := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' }) {
		k, err := strconv.Atoi(w)
		if err != nil || k < 1 || k > n {
			return nil, false
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k-1)
		}
	}
	return out, len(out) > 0
}

func confirm(question string) bool {
	tty, err := openTerminal()
	if err != nil {
		return false
	}
	defer tty.Close()
	fmt.Fprint(os.Stderr, question)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y")
}

// cmdRequests implements `plug [-p profile] requests`: the keys waiting, and
// the ones the member picks are accepted.
func cmdRequests(args []string) {
	cfg, rest := admissionTarget("requests", args)
	if len(rest) > 0 {
		fatal("requests takes no argument; to accept one key: plug -p %s accept <key>", clusterName(cfg))
	}
	rows := parseRows(askAgent(&cfg, "requests"), "request")
	if len(rows) == 0 {
		info("%s: no request is waiting", clusterName(cfg))
		return
	}
	now := time.Now()
	for i, r := range rows {
		fmt.Fprintf(os.Stderr, "  %d  %-24s %s…  %-10s %s\n", i+1, r.name, shortKey(r.fp), ago(r.last, now), r.from)
	}
	picked := choose(fmt.Sprintf("Accept which? (1-%d, all, Enter to cancel): ", len(rows)), len(rows), true)
	for _, i := range picked {
		acceptKey(&cfg, rows[i].fp)
	}
}

// cmdAccept implements `plug [-p profile] accept <key>`: the line a newcomer's
// refusal tells them to send.
func cmdAccept(args []string) {
	cfg, rest := admissionTarget("accept", args)
	if len(rest) != 1 {
		fatal("usage: plug -p <profile> accept <key>   (the key the newcomer's plug printed)")
	}
	acceptKey(&cfg, keyArg(&cfg, rest[0], "requests", "request"))
}

func acceptKey(cfg *config, key string) {
	out := askAgent(cfg, "accept "+key)
	if f := strings.Fields(out); len(f) >= 3 && f[0] == "ok" {
		info("%s is a member of %s", f[2], clusterName(*cfg))
		return
	}
	fatal("unexpected answer from the agent: %q", out)
}

// cmdRevoke implements `plug [-p profile] revoke <key>`: the one a script
// runs, with the key `plug members` shows. The menu asks; a script cannot.
func cmdRevoke(args []string) {
	cfg, rest := admissionTarget("revoke", args)
	if len(rest) != 1 {
		fatal("usage: plug -p <profile> revoke <key>   (the key 'plug -p %s members' shows)", clusterName(cfg))
	}
	revokeKey(&cfg, keyArg(&cfg, rest[0], "members", "member"))
}

// keyArg is the key a person typed, as the agent must receive it. Git Bash
// rewrites an argument it takes for a POSIX path before plug.exe sees it: a
// key printed as +/Blct9xqTYw arrives as +C:/Program Files/Git/Blct9xqTYw, and
// the agent, reading three words, answered with its usage line. A fingerprint
// is base64 and has no drive letter, no colon and no space, so a key with any
// of them was rewritten, and the key is the one tail of that path that names
// a key the agent lists (verb, rows of kind).
func keyArg(cfg *config, arg, verb, kind string) string {
	if isKeyShape(arg) {
		return arg
	}
	var fps []string
	for _, r := range parseRows(askAgent(cfg, verb), kind) {
		fps = append(fps, r.fp)
	}
	if key, ok := unrewrittenKey(arg, fps); ok {
		return key
	}
	fatal("%q is not a key. Git Bash rewrites an argument that starts like a path: run the command again with MSYS_NO_PATHCONV=1 in front of it", arg)
	return ""
}

// isKeyShape: SHA256: optional, then base64 only.
func isKeyShape(s string) bool {
	s = strings.TrimPrefix(s, "SHA256:")
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
			return false
		}
	}
	return s != ""
}

// unrewrittenKey undoes Git Bash's rewrite of a key against the fingerprints
// it could name. The rewrite keeps the leading + it skipped, then puts the
// install directory where the first / was. Where that directory ends is not
// known, so each tail after a separator is tried, and the longest that names
// exactly one fingerprint is the key: a shorter tail that also matches is a
// piece of the key, never another key.
func unrewrittenKey(arg string, fps []string) (string, bool) {
	lead := len(arg) - len(strings.TrimLeft(arg, "+"))
	path := strings.ReplaceAll(arg[lead:], `\`, "/")
	if len(path) < 3 || path[1] != ':' || path[2] != '/' {
		return "", false
	}
	for i := 2; i < len(path); i++ {
		if path[i] != '/' {
			continue
		}
		key := arg[:lead] + path[i:]
		if !isKeyShape(key) || len(key) < 12 {
			continue
		}
		n := 0
		for _, fp := range fps {
			if strings.HasPrefix(strings.TrimPrefix(fp, "SHA256:"), key) {
				n++
			}
		}
		if n == 1 {
			return key, true
		}
	}
	return "", false
}

func revokeKey(cfg *config, key string) {
	out := askAgent(cfg, "revoke "+key)
	if f := strings.Fields(out); len(f) >= 3 && f[0] == "ok" {
		info("%s is no longer a member of %s", f[2], clusterName(*cfg))
		return
	}
	fatal("unexpected answer from the agent: %q", out)
}

// cmdMembers implements `plug [-p profile] members`: who is in, and the ones
// the member picks are revoked, after a confirmation each.
func cmdMembers(args []string) {
	cfg, rest := admissionTarget("members", args)
	if len(rest) > 0 {
		fatal("members takes no argument")
	}
	rows := parseRows(askAgent(&cfg, "members"), "member")
	now := time.Now()
	for i, r := range rows {
		by := "accepted by " + r.by
		if r.by == "-" || r.by == "" {
			by = "first member"
		}
		// Who accepted last: a name has no width limit a column could hold.
		fmt.Fprintf(os.Stderr, "  %d  %-24s %s…  since %s  seen %-10s %s\n",
			i+1, r.name, shortKey(r.fp), r.since.Local().Format("2006-01-02"), ago(r.seen, now), by)
	}
	picked := choose(fmt.Sprintf("Revoke which? (1-%d, Enter to cancel): ", len(rows)), len(rows), false)
	for _, i := range picked {
		if !confirm(fmt.Sprintf("Revoke %s? [y/N] ", rows[i].name)) {
			continue
		}
		revokeKey(&cfg, rows[i].fp)
	}
}
