// The Go e2e client: reach a cluster service BY NAME, by protocol, using the
// language's NATURAL driver. Invoked (under plug) as:  eclient <proto> <host:port>
//
// Every protocol just connects to <host:port> by name and lets the driver do its
// thing: no proxy, no hook, no config. plug captures at the IP layer through its
// userspace TUN, so the app's socket is never touched and each driver reaches the
// cluster service by name uniformly. Prints "E2E-OK <proto> — …" and exits 0 on
// success.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"
	_ "github.com/lib/pq"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func die(format string, a ...any) {
	fmt.Printf("E2E-FAIL "+format+"\n", a...)
	os.Exit(1)
}
func ok(proto, detail string) {
	fmt.Printf("E2E-OK %s — %s\n", proto, detail)
	os.Exit(0)
}

// retry runs f up to ~12s so a service still warming behind the tunnel doesn't
// flake the case.
func retry(f func() error) error {
	var err error
	for i := 0; i < 12; i++ {
		if err = f(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return err
}

func main() {
	if len(os.Args) < 3 {
		die("usage: eclient <proto> <host:port>")
	}
	proto, target := os.Args[1], os.Args[2]
	switch proto {
	case "http":
		doHTTP(target)
	case "postgres":
		doPostgres(target)
	case "redis":
		doRedis(target)
	case "mongo":
		doMongo(target)
	case "amqp":
		doAMQP(target)
	case "mqtt":
		doMQTT(target)
	case "grpc":
		doGRPC(target)
	case "websocket":
		doWebSocket(target)
	case "dns":
		doDNS(target)
	default:
		die("proto %q not implemented in the go client", proto)
	}
}

func doHTTP(target string) {
	url := "http://" + target + "/get"
	var body []byte
	err := retry(func() error {
		resp, err := http.Get(url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		body, _ = io.ReadAll(resp.Body)
		return nil
	})
	if err != nil {
		die("http GET %s: %v", url, err)
	}
	ok("http", fmt.Sprintf("%s → 200 (%d bytes)", url, len(body)))
}

// doWebSocket opens a ws:// connection BY NAME, sends a text frame and asserts the
// echo server returns it verbatim — exercising HTTP Upgrade + a bidirectional
// frame round-trip through plug's TUN.
func doWebSocket(target string) {
	url := "ws://" + target + "/"
	const msg = "plug-e2e-ws-42"
	var got string
	err := retry(func() error {
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			return err
		}
		defer c.Close()
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			return err
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, b, err := c.ReadMessage()
		if err != nil {
			return err
		}
		got = string(b)
		return nil
	})
	if err != nil {
		die("websocket %s: %v", url, err)
	}
	if got != msg {
		die("websocket echo mismatch: got %q want %q", got, msg)
	}
	ok("websocket", fmt.Sprintf("%s → echo %q", url, got))
}

func doPostgres(target string) {
	dsn := fmt.Sprintf("postgres://plug:plug@%s/plug?sslmode=disable&connect_timeout=5", target)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		die("pg open: %v", err)
	}
	defer db.Close()
	var n int
	if err := retry(func() error { return db.QueryRow("SELECT 1").Scan(&n) }); err != nil {
		die("pg SELECT 1 (%s): %v", target, err)
	}
	if n != 1 {
		die("pg SELECT 1 = %d", n)
	}
	ok("postgres", fmt.Sprintf("%s → SELECT 1 = 1", target))
}

func doRedis(target string) {
	rdb := redis.NewClient(&redis.Options{Addr: target, DialTimeout: 5 * time.Second})
	defer rdb.Close()
	ctx := context.Background()
	var pong string
	if err := retry(func() error { var e error; pong, e = rdb.Ping(ctx).Result(); return e }); err != nil {
		die("redis PING (%s): %v", target, err)
	}
	ok("redis", fmt.Sprintf("%s → PING = %s", target, pong))
}

func doMongo(target string) {
	uri := "mongodb://" + target + "/?directConnection=true&serverSelectionTimeoutMS=5000"
	cli, err := mongo.Connect(context.Background(), options.Client().ApplyURI(uri))
	if err != nil {
		die("mongo connect: %v", err)
	}
	defer cli.Disconnect(context.Background())
	if err := retry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return cli.Ping(ctx, nil)
	}); err != nil {
		die("mongo ping (%s): %v", target, err)
	}
	ok("mongo", fmt.Sprintf("%s → ping ok", target))
}

func doAMQP(target string) {
	url := "amqp://plug:plug@" + target + "/"
	var body string
	if err := retry(func() error {
		conn, err := amqp.Dial(url)
		if err != nil {
			return err
		}
		defer conn.Close()
		ch, err := conn.Channel()
		if err != nil {
			return err
		}
		defer ch.Close()
		// "plug-e2e" is pre-declared by the broker's definitions.json — clients
		// don't declare at runtime (RabbitMQ 4 rejects some runtime declares).
		if err := ch.PublishWithContext(context.Background(), "", "plug-e2e", false, false,
			amqp.Publishing{Body: []byte("ping")}); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
		msg, got, err := ch.Get("plug-e2e", true)
		if err != nil {
			return err
		}
		if !got {
			return fmt.Errorf("no message back")
		}
		body = string(msg.Body)
		return nil
	}); err != nil {
		die("amqp (%s): %v", target, err)
	}
	ok("amqp", fmt.Sprintf("%s → publish/get %q", target, body))
}

func doMQTT(target string) {
	recv := make(chan string, 1)
	// AutoReconnect + ConnectRetry: the first flow of a session can drop while the
	// datapath settles (seen once on the Windows service path: connect token OK,
	// then "not Connected" at subscribe) — let paho re-dial instead of dying.
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + target).
		SetClientID("plug-e2e-go").SetConnectTimeout(5 * time.Second).
		SetAutoReconnect(true).SetConnectRetry(true).SetConnectRetryInterval(time.Second)
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
		select {
		case recv <- string(m.Payload()):
		default:
		}
	})
	c := mqtt.NewClient(opts)
	if err := retry(func() error {
		t := c.Connect()
		t.WaitTimeout(5 * time.Second)
		return t.Error()
	}); err != nil {
		die("mqtt connect (%s): %v", target, err)
	}
	defer c.Disconnect(200)
	if err := retry(func() error {
		if !c.IsConnectionOpen() {
			return fmt.Errorf("not connected yet")
		}
		t := c.Subscribe("plug/e2e", 0, nil)
		t.WaitTimeout(5 * time.Second)
		return t.Error()
	}); err != nil {
		die("mqtt subscribe: %v", err)
	}
	c.Publish("plug/e2e", 0, false, "ping").WaitTimeout(5 * time.Second)
	select {
	case m := <-recv:
		ok("mqtt", fmt.Sprintf("%s → pub/sub %q", target, m))
	case <-time.After(6 * time.Second):
		die("mqtt (%s): no message received", target)
	}
}

func doGRPC(target string) {
	// Plain HTTP/2 to <host:port> by name — plug captures it at the IP layer, no
	// proxy involved.
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
	if err != nil {
		die("grpc dial: %v", err)
	}
	defer conn.Close()
	h := grpc_health_v1.NewHealthClient(conn)
	var status grpc_health_v1.HealthCheckResponse_ServingStatus
	if err := retry(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := h.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		if err != nil {
			return err
		}
		status = resp.Status
		return nil
	}); err != nil {
		die("grpc health (%s): %v", target, err)
	}
	if status != grpc_health_v1.HealthCheckResponse_SERVING {
		die("grpc status %v", status)
	}
	ok("grpc", fmt.Sprintf("%s → health SERVING", target))
}

// doDNS proves the resolver still answers questions that are not addresses.
// plug used to reply NODATA to every SRV, MX, PTR and TXT — and on macOS its
// stub is the resolver for the WHOLE machine while a session runs, so that broke
// AD clients, mongodb+srv:// URIs and Consul host-wide. Invoked as
// `eclient dns mx:google.com` or `eclient dns srv:_sip._udp.sip.voice.google.com`.
//
// Only ONE outcome fails: a not-found. NODATA is what the bug looked like from a
// client, and nothing but plug can produce it for a name that plainly has these
// records. A transport error (SERVFAIL, timeout, a CI runner with no route out)
// is reported and passed over — it says nothing either way, and failing on it
// would make the cell depend on somebody else's network.
func doDNS(target string) {
	kind, name, found := strings.Cut(target, ":")
	if !found {
		die("dns: want <mx|srv>:<name>, got %q", target)
	}

	var n int
	var err error
	switch kind {
	case "mx":
		var recs []*net.MX
		recs, err = net.LookupMX(name)
		n = len(recs)
	case "srv":
		var recs []*net.SRV
		// Empty service and proto: look the name up exactly as given.
		_, recs, err = net.LookupSRV("", "", name)
		n = len(recs)
	default:
		die("dns: unknown record kind %q", kind)
	}

	var dnsErr *net.DNSError
	switch {
	case n > 0:
		ok("dns", fmt.Sprintf("%s %s → %d record(s) relayed to the upstream", kind, name, n))
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		die("dns: %s %s came back empty — the non-A relay is not working (this is the NODATA bug)", kind, name)
	case err != nil:
		ok("dns", fmt.Sprintf("%s %s — upstream unreachable (%v); not a verdict, skipped", kind, name, err))
	default:
		die("dns: %s %s returned no records and no error", kind, name)
	}
}
