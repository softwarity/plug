package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// cmdPrune implements `plug prune`: delete every cached core no cluster runs
// any more. The cache under ~/.plug/versions grows one directory per version
// ever met — releases and dev builds alike — and nothing ever shrank it.
//
// What is "in use" is the AGENTS' answer, not a guess from mtimes: each
// profile's agent is asked which version it runs, and those versions stay.
// Everything else goes — a deleted core costs nothing but a re-download from
// the agent, which is where it came from in the first place.
func cmdPrune() {
	names := listProfiles()
	if len(names) == 0 {
		fatal("no profile configured — nothing tells plug which versions are still in use")
	}

	// Ask every cluster in parallel, like `plug versions` does.
	type answer struct{ name, version, note string }
	ch := make(chan answer, len(names))
	for _, n := range names {
		go func(n string) {
			cfg, err := readProfileSoft(n)
			if err != nil {
				ch <- answer{n, "", "broken profile (" + err.Error() + ")"}
				return
			}
			v, err := agentVersionTimeout(cfg, 5*time.Second)
			if err != nil {
				ch <- answer{n, "", fmt.Sprintf("unreachable (%s:%s)", cfg.host, cfg.port)}
				return
			}
			ch <- answer{n, v, ""}
		}(n)
	}
	active := map[string]bool{}
	var silent []string
	for range names {
		a := <-ch
		if a.version != "" {
			info("%s: agent v%s — keeping that core", a.name, shortVersion(a.version))
			active[a.version] = true
		} else {
			info("%s: %s", a.name, a.note)
			silent = append(silent, a.name)
		}
	}

	// The store moved on some platforms, and a cached core is disposable — it is
	// re-downloaded on demand and verified on every launch — so nothing was
	// migrated. What must not happen is leaving the old place behind for a prune
	// that no longer looks at it: clear it here, unconditionally. Every version
	// in there is by definition one no cluster is served from any more.
	if old := legacyVersionsDir(); old != "" {
		if entries, err := os.ReadDir(old); err == nil && len(entries) > 0 {
			freed := dirSize(old)
			if err := os.RemoveAll(old); err != nil {
				info("could not clear the old cache at %s (%v)", old, err)
			} else {
				info("cleared %d core(s) from the old cache at %s (%.1f MB)", len(entries), old, float64(freed)/(1<<20))
			}
		}
	}

	entries, err := os.ReadDir(versionsDir())
	if err != nil {
		info("nothing cached — %s does not exist yet", versionsDir())
		return
	}
	var cached []string
	for _, e := range entries {
		if e.IsDir() {
			cached = append(cached, e.Name())
		}
	}

	victims, reason := pruneVictims(cached, active)
	if reason != "" {
		fatal("%s", reason)
	}
	if len(victims) == 0 {
		info("nothing to prune — every cached core is what some cluster runs")
		return
	}
	if len(silent) > 0 {
		// Their versions cannot be protected — said before deleting, not after,
		// so an "unreachable because it is my VPN that is down" moment is a
		// visible reason to Ctrl-C rather than a surprise. The cost of being
		// wrong stays small either way: the next session re-downloads.
		info("WARNING %d profile(s) did not answer — a version only they use cannot be recognised as active", len(silent))
	}

	freed := int64(0)
	removed := 0
	for _, v := range victims {
		dir := filepath.Join(versionsDir(), v)
		freed += dirSize(dir)
		if err := os.RemoveAll(dir); err != nil {
			// Windows refuses to delete a running binary — a live session on a
			// version no reachable agent claims (its cluster just went down,
			// say). Skip it and say so; it stays prunable once it exits.
			info("cannot remove v%s (%v) — in use? skipped", v, err)
			continue
		}
		removed++
		info("removed v%s", shortVersion(v))
	}
	fmt.Printf("pruned %d version(s), freed %s — %d kept\n", removed, humanBytes(freed), len(cached)-removed)
}

// pruneVictims decides, and only decides — no I/O, so the rule is testable.
// It refuses to act when NOT ONE agent answered: with an empty active set every
// cached core would qualify, and "my laptop is offline" must not read as
// "delete everything".
func pruneVictims(cached []string, active map[string]bool) (victims []string, refusal string) {
	if len(active) == 0 {
		return nil, "no agent answered — refusing to prune with nothing marked in use (are you offline?)"
	}
	for _, v := range cached {
		if !active[v] {
			victims = append(victims, v)
		}
	}
	sort.Strings(victims)
	return victims, ""
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, ferr := d.Info(); ferr == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
