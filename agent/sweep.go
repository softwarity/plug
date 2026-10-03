package agent

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// The sweep: what the boot gc and the periodic sweep do to the objects a
// session leaves behind, in ONE place.
//
// Every backend used to carry its own loop, six of them with the same shape:
// list by label, keep what a live session still uses, restore what a dead one
// parked, remove the rest. Each had its own reading of the rules, and the
// readings drifted: one deleted a signpost whose restore had failed (the
// receipt went with it, the workload stayed stopped), another judged a
// lingering name by ownership and killed the address the linger existed to
// keep. The rules are here now and apply to every kind alike; a backend only
// says how to list its objects and what each one can do.

// sweepItem is one object a sweep considers, with what the sweep needs to know
// about it and what it may do to it. The hooks a kind has no use for stay nil:
// a mount helper parks nothing, never lingers and belongs to whoever's session
// answers, so it supplies live and drop and nothing else.
type sweepItem struct {
	// key is what a failed restore is noted under: one per receipt, for the
	// life of the process (gcNoteOnce). Only read when restore is set.
	key string
	// stamp is the linger stamp, "" when the object is not lingering.
	stamp string
	// live reports whether the session holding the object still answers.
	live func() bool
	// held reports whether a live OTHER agent owns the object, which puts it
	// out of this sweep's reach: a co-located agent's signpost is not a
	// leftover because it does not answer in this agent's network namespace.
	// nil: nobody else's.
	held func() bool
	// serving runs on an object whose session answers: what a live session may
	// still need put right (a Kubernetes takeover whose controller slice is
	// still beside plug's). nil: nothing.
	serving func()
	// restore puts back what the object parked. On success it returns the line
	// that closes an earlier failure note (said only if there was one); on
	// failure the error's text is the note, said once per key. nil: the object
	// parks nothing.
	restore func() (recovered string, err error)
	// drop removes the object outright: a linger past its grace, or, when
	// retire is nil, a dead session's object once what it parked is back.
	drop func()
	// retire is what a dead session's object becomes INSTEAD of being dropped:
	// a Kubernetes name lingers, keeping its ClusterIP for the re-arm that
	// commonly follows an agent restart. nil: drop.
	retire func()
}

// sweepTarget lists one kind of object for the sweep. It is a function so
// that the backend decides what a failed listing means (dockerGC says nothing,
// k8sGC notes it) and sweep never sees the difference.
type sweepTarget func() []sweepItem

// sweep applies the rules to every object the target lists, in this order:
//
//  1. A lingering object is judged by its stamp alone, before anything else:
//     within the grace it stays, whoever stamped it; past the grace it goes,
//     whoever stamped it. Ownership would misjudge it: an agent restart renames
//     the owner, and a linger read as an orphan would be swept with the very
//     address it exists to keep.
//  2. An object whose session still answers is in use and is left alone. Before
//     ownership, because the owner label names a ROLE: every replica of one
//     deployment reads its siblings' live objects as "mine".
//  3. An object a live OTHER agent owns is not this sweep's to touch.
//  4. What a dead session parked is restored FIRST, and the object only goes
//     once that succeeded: the receipt lives in the object's labels, and an
//     object deleted on a failed restore leaves a workload stopped with nothing
//     anywhere saying a session stopped it. A failed restore is said once per
//     receipt and per boot (the sweep runs every minute), and the recovery once
//     when it comes.
//  5. The dead session's object is retired: dropped, or set to linger.
func sweep(target sweepTarget) {
	now := time.Now()
	for _, it := range target() {
		if it.stamp != "" {
			if lingerExpired(it.stamp, now) && it.drop != nil {
				it.drop()
			}
			continue
		}
		if it.live != nil && it.live() {
			if it.serving != nil {
				it.serving()
			}
			continue
		}
		if it.held != nil && it.held() {
			continue
		}
		if it.restore != nil {
			recovered, err := it.restore()
			if err != nil {
				gcNoteOnce(it.key, "%v", err)
				continue
			}
			if recovered != "" {
				gcNoteRecovered(it.key, "%s", recovered)
			}
		}
		switch {
		case it.retire != nil:
			it.retire()
		case it.drop != nil:
			it.drop()
		}
	}
}

// lingering narrows a target to the objects that carry a linger stamp: what
// the serve path reaps on its way (an agent that never restarts must not keep
// dead names resolving for ever), and ONLY that. Serving one name must not
// restore what another name's dead session parked; that is the sweep's job,
// on its own clock.
func lingering(target sweepTarget) sweepTarget {
	return func() []sweepItem {
		var out []sweepItem
		for _, it := range target() {
			if it.stamp != "" {
				out = append(out, it)
			}
		}
		return out
	}
}

// lingerGrace is how long an unserved name stays warm before the sweep reaps
// it. Derived, not felt: it must outlive the 600s TTL Docker's DNS handed to
// every caller, otherwise the linger protects nothing, and fifteen minutes
// gives margin without keeping dead names resolving for hours.
const lingerGrace = 15 * time.Minute

// lingerLabel stamps WHEN the name was unserved (unix seconds): as a Swarm
// service label, and verbatim as a k8s annotation key.
const lingerLabel = "plug.linger.since"

func lingerStamp() string { return strconv.FormatInt(time.Now().Unix(), 10) }

// lingerExpired reports whether a stamp is past the grace. Empty means "not
// lingering" (a live session's signpost, or a crash leftover: the sweep's other
// rules own those). An unreadable stamp reads as expired: reaping is the honest
// direction for a label something has mangled.
func lingerExpired(stamp string, now time.Time) bool {
	if stamp == "" {
		return false
	}
	n, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return true
	}
	return now.Sub(time.Unix(n, 0)) > lingerGrace
}

// gcNote reports a sweep failure on stderr, the container's log, which is
// where anyone hunting "why is my service still scaled to 0" will look. The
// sweep is best-effort by design (a crashed session's leftovers), but
// best-effort must not mean invisible: its whole job is restoring workloads a
// dead session parked.
func gcNote(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "plug-agent gc: "+format+"\n", a...)
}

// gcNoted remembers what the sweep already said, by receipt, for the life of
// this process. The sweep runs every minute (sweepPeriodically) and a restore
// that fails keeps failing until someone acts, so the same line would otherwise
// fill the log once a minute and bury everything else. One line when it starts
// failing, one when it recovers, and the receipt stays in place in between.
var (
	gcNotedMu sync.Mutex
	gcNoted   = map[string]bool{}
)

// gcNoteOnce says it the first time only, per key and per boot.
func gcNoteOnce(key, format string, a ...any) {
	gcNotedMu.Lock()
	seen := gcNoted[key]
	gcNoted[key] = true
	gcNotedMu.Unlock()
	if !seen {
		gcNote(format, a...)
	}
}

// gcNoteRecovered closes a gcNoteOnce: said only when there was a failure to
// close, so an ordinary restore stays as quiet as it always was.
func gcNoteRecovered(key, format string, a ...any) {
	gcNotedMu.Lock()
	seen := gcNoted[key]
	delete(gcNoted, key)
	gcNotedMu.Unlock()
	if seen {
		gcNote(format, a...)
	}
}
