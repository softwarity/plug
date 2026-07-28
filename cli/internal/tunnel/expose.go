package tunnel

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// The reverse direction: the agent listens on 0.0.0.0:<ClusterPort> (reached by
// the other workloads through a pre-declared cluster DNS name — a network alias
// on the agent service in Compose/Swarm, a Service selecting the agent pod in
// Kubernetes) and every connection is relayed down this session's SSH
// connection to 127.0.0.1:<LocalPort>. sshd does all the server-side work
// (standard remote forward), so the listener lives and dies with the session:
// Ctrl-C and the port closes, nothing to clean up cluster-side.

// ExposeSpec is one -s mapping: <Name>:<ClusterPort>:<LocalPort>.
type ExposeSpec struct {
	Name        string // cluster DNS name — used for messages and the path check
	ClusterPort string
	LocalPort   string
	// PortVar is set when the third field named a variable instead of a port
	// (-s web:8080:PORT): plug picks a free local port, fills LocalPort in, and
	// hands the number to the child as $PortVar and {PortVar}. Carried on the
	// spec because it IS the third field's other spelling; nothing in this
	// package reads it — by the time a spec is armed, LocalPort is resolved.
	PortVar string
}

func (s ExposeSpec) String() string {
	return s.Name + ":" + s.ClusterPort + " -> localhost:" + s.LocalPort
}

// exposeRearmEvery paces re-bind attempts after a reconnect: the agent's sshd
// may hold the dead connection's bind until its keepalive gives up on it
// (ClientAliveInterval×CountMax server-side, ~90s worst case).
const exposeRearmEvery = 5 * time.Second

// Exposed is one armed mapping. Verify checks the full path once; the
// background goroutine keeps the mapping armed across transport reconnects.
type Exposed struct {
	t    *Transport
	spec ExposeSpec

	// Self-test state: while `nonce` is non-nil, each accepted connection is
	// sniffed for the nonce before being relayed; Verify disarms it afterwards
	// so steady-state accepts pay zero overhead.
	nonce atomic.Pointer[[]byte]
	hit   chan struct{}

	// rearmHook re-provisions a DYNAMIC name after a reconnect: rearm re-binds
	// only the sshd forward, but a restarted agent GC's the signpost, so the
	// name must be re-created (and re-verified). nil for static names. Set once
	// before serve() starts re-arming, so no lock is needed.
	rearmHook func() error
}

// OnRearm registers a hook run after every reconnect re-binds the forward — used
// to re-provision a dynamic name a restarted agent may have swept. Must be set
// before the first reconnect (right after Expose), which the single-threaded
// startExposes guarantees.
func (e *Exposed) OnRearm(hook func() error) { e.rearmHook = hook }

// Expose arms spec on the transport: it binds 0.0.0.0:<ClusterPort> agent-side
// and relays every connection to 127.0.0.1:<LocalPort>. The first bind failure
// is returned synchronously (a taken port — another session? — must fail loud);
// after a transport reconnect the mapping re-arms itself in the background.
func (t *Transport) Expose(spec ExposeSpec) (*Exposed, error) {
	cl := t.current()
	if cl == nil {
		var err error
		if cl, err = t.reconnectFrom(nil); err != nil {
			return nil, err
		}
	}
	ln, err := listenRemote(cl, spec)
	if err != nil {
		return nil, err
	}
	e := &Exposed{t: t, spec: spec, hit: make(chan struct{}, 1)}
	go e.serve(ln, cl)
	return e, nil
}

func listenRemote(cl *ssh.Client, spec ExposeSpec) (net.Listener, error) {
	ln, err := cl.Listen("tcp", net.JoinHostPort("0.0.0.0", spec.ClusterPort))
	if err != nil {
		return nil, fmt.Errorf("agent refused to open %s:%s (already exposed by another session?): %w",
			spec.Name, spec.ClusterPort, err)
	}
	return ln, nil
}

// serve accepts until the SSH connection under ln dies, then re-arms the
// mapping on a healthy connection and keeps going — the same self-heal
// contract as the forward direction. Only a Close()d transport (the session
// ending) stops it.
func (e *Exposed) serve(ln net.Listener, cl *ssh.Client) {
	for {
		for {
			conn, err := ln.Accept()
			if err != nil {
				break // the connection under this listener is gone (or transport Close())
			}
			go e.handle(conn)
		}
		var err error
		if ln, cl, err = e.rearm(cl); err != nil {
			return
		}
		// The sshd forward is re-bound; but if the agent restarted it GC'd the
		// dynamic signpost, so re-provision the name and re-verify. Run it in the
		// background: the Accept loop above must be live to catch Verify's nonce.
		if e.rearmHook != nil {
			go func() {
				if err := e.rearmHook(); err != nil {
					e.t.note("expose %s: re-provision after reconnect FAILED (%v) — the name may be unreachable", e.spec, err)
				} else {
					e.t.note("expose %s: name re-provisioned and re-verified after reconnect", e.spec)
				}
			}()
		}
	}
}

// rearm re-establishes the remote listener after the connection under it died.
// dead is the only client ever handed to reconnectFrom, so a live shared
// connection is never torn down (reconnectFrom returns the current client
// untouched once someone — us or the keepalive — has replaced dead). The bind
// retry never gives up while the session lives: after a rough disconnect the
// agent's sshd can hold the dead bind for ~90s, and a port taken by another
// session may free up hours later. Only a Close()d transport ends the loop.
func (e *Exposed) rearm(dead *ssh.Client) (net.Listener, *ssh.Client, error) {
	for attempt := 0; ; attempt++ {
		cl, err := e.t.reconnectFrom(dead)
		if err == nil {
			ln, lerr := listenRemote(cl, e.spec)
			if lerr == nil {
				e.t.note("expose %s re-armed", e.spec)
				return ln, cl, nil
			}
			// Bind still held server-side, or this client died meanwhile (then
			// the transport keepalive will swap in a fresh one for the next
			// round). Either way: wait and retry.
			err = lerr
		} else if errors.Is(err, errClosed) {
			return nil, nil, err // session ending
		}
		// else: transient dial failure (network still down) — keep trying.
		if attempt%12 == 0 { // one note a minute, not one every 5s
			e.t.note("expose %s: re-arming… (%v)", e.spec, err)
		}
		time.Sleep(exposeRearmEvery)
	}
}

// handle relays one accepted connection. While the self-test nonce is armed
// (the short Verify window at startup), the first bytes are sniffed so Verify
// can tell "the full path loops back to THIS session" from "something else
// answered". SSH channel conns have no read deadlines (SetReadDeadline is a
// no-op error in x/crypto), so the sniff is bounded by racing the read against
// a timer: a flow that hasn't spoken within the grace is spliced to the local
// service with its pending read re-attached — a server-first protocol sees the
// local greeting immediately and loses nothing.
func (e *Exposed) handle(conn net.Conn) {
	np := e.nonce.Load()
	if np == nil {
		e.relayLocal(conn, nil, nil)
		return
	}
	buf := make([]byte, len(*np))
	nc := make(chan int, 1)
	go func() { n, _ := io.ReadFull(conn, buf); nc <- n }()
	select {
	case n := <-nc:
		if n == len(*np) && bytes.Equal(buf, *np) {
			select {
			case e.hit <- struct{}{}:
			default:
			}
			conn.Close()
			return
		}
		e.relayLocal(conn, buf[:n], nil)
	case <-time.After(500 * time.Millisecond):
		e.relayLocal(conn, buf, nc)
	}
}

// relayLocal dials the local service and splices conn to it, with the same
// half-close semantics as relay(). sniffed bytes already consumed from conn
// are written to the local side first; if pending is non-nil the sniff read is
// still in flight — the client→local direction waits for it while local→client
// flows immediately.
func (e *Exposed) relayLocal(conn net.Conn, sniffed []byte, pending <-chan int) {
	local, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", e.spec.LocalPort), 5*time.Second)
	if err != nil {
		// The local service isn't up (yet) — refuse this flow, exactly like a
		// container whose process hasn't bound its port. Closing conn also
		// unblocks a pending sniff read.
		conn.Close()
		return
	}
	closeWrite := func(c net.Conn) {
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			c.Close()
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // client → local (delivers the sniffed prefix first)
		defer wg.Done()
		prefix := sniffed
		if pending != nil {
			prefix = sniffed[:<-pending]
		}
		if len(prefix) > 0 {
			if _, err := local.Write(prefix); err != nil {
				conn.Close()
				local.Close()
				return
			}
		}
		io.Copy(local, conn)
		closeWrite(local)
	}()
	go func() { // local → client
		defer wg.Done()
		io.Copy(conn, local)
		closeWrite(conn)
	}()
	wg.Wait()
	conn.Close()
	local.Close()
}

// Verify proves the full loop once: it dials <Name>:<ClusterPort> through the
// agent — resolving the name exactly as any cluster workload would — writes a
// nonce, and checks the nonce lands back on THIS session's listener.
func (e *Exposed) Verify() error {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil // can't verify, don't block the session on it
	}
	// Drain a late hit from a PRIOR attempt: its buffered token would otherwise
	// satisfy this attempt's select without our nonce ever arriving (false pass).
	select {
	case <-e.hit:
	default:
	}
	e.nonce.Store(&nonce)
	// Disarm a beat AFTER returning, but ONLY if it is still OUR nonce: a prior
	// attempt's timer must not wipe a live retry's nonce (which would relay the
	// retry's nonce into the local service and time the check out). A nonce still
	// in flight past our timeout is recognized and dropped, not forwarded.
	defer time.AfterFunc(2*time.Second, func() { e.nonce.CompareAndSwap(&nonce, nil) })

	conn, err := e.t.DialCluster(net.JoinHostPort(e.spec.Name, e.spec.ClusterPort))
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "refused") {
			return fmt.Errorf("%s resolves inside the cluster but %s:%s refuses the connection — either the "+
				"agent image predates -s (its sshd bound the loopback: update the agent image), or the name "+
				"resolves to another container that doesn't listen on that port",
				e.spec.Name, e.spec.Name, e.spec.ClusterPort)
		}
		return fmt.Errorf("%s is not reachable inside the cluster (%v) — declare the name on the agent "+
			"(a network alias in your stack file, or a Service selecting the agent pod on Kubernetes)",
			e.spec.Name, err)
	}
	defer conn.Close()
	if _, err := conn.Write(nonce); err != nil {
		return fmt.Errorf("%s:%s answered but the check could not complete: %v",
			e.spec.Name, e.spec.ClusterPort, err)
	}
	select {
	case <-e.hit:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New(e.spec.Name + ":" + e.spec.ClusterPort +
			" is answered by something else — the real service still running in the stack " +
			"(DNS round-robins between it and you), another session exposing it, or a very slow path")
	}
}
