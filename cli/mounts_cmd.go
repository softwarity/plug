package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// cmdMounts is `plug mounts`: what the sessions alive on this machine have
// mounted, and where. Most mounts are not asked for, they are what a takeover
// places under a session directory so the process finds its data where its
// environment says, and that is exactly why a person needs this list: to open
// the volume in an editor or a file browser, the path has to be found
// somewhere. Dead sessions are not shown; their leftovers are the next run's
// and doctor's business (sweepOrphanMounts, doctorMounts).
func cmdMounts() {
	rows := liveMounts(mountRecords(), processAlive, mountedAt)
	if len(rows) == 0 {
		fmt.Println("no volume mounted by a live plug session on this machine")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tVOLUME\tCLUSTER\tSESSION\tSTATE")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.path, r.volume, r.cluster, r.session, r.state)
	}
	w.Flush()
}

// mountRow is one line of `plug mounts`, computed apart from the printing so
// the shape is tested without a mount.
type mountRow struct {
	path, volume, cluster, session, state string
}

// liveMounts keeps the records whose session is alive, sorted by path, and
// words each one. The spec is "name:volume:path" as mountSpec prints it; the
// volume column shows "name:volume", the part that says which data this is.
func liveMounts(records []mountRecord, alive func(int) bool, mounted func(string) bool) []mountRow {
	var out []mountRow
	for _, r := range records {
		if r.pid == 0 || !alive(r.pid) {
			continue
		}
		volume := r.spec
		if i := strings.LastIndex(r.spec, ":"+r.path); i > 0 && strings.HasSuffix(r.spec, r.path) {
			volume = r.spec[:i]
		}
		session := fmt.Sprintf("pid %d", r.pid)
		if r.auto {
			session += ", automatic"
		} else {
			session += ", --mount"
		}
		state := "mounted"
		if !mounted(r.path) {
			state = "not mounted (the session still holds the record)"
		}
		cluster := r.cluster
		if cluster == "" {
			cluster = "?"
		}
		out = append(out, mountRow{path: r.path, volume: volume, cluster: cluster, session: session, state: state})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}
