// The Go e2e client: reach a cluster service BY NAME, by protocol, using the
// language's NATURAL driver. Invoked (under plug) as:  eclient <proto> <host:port>
//
// http goes through Go's net/http (honors HTTP_PROXY → exercises the proxy path);
// every other protocol is a raw-TCP driver (no proxy honoring) → it exercises the
// seccomp supervisor. Prints "E2E-OK <proto> — …" and exits 0 on success.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
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
		q, err := ch.QueueDeclare("plug-e2e", false, true, false, false, nil)
		if err != nil {
			return err
		}
		if err := ch.PublishWithContext(context.Background(), "", q.Name, false, false,
			amqp.Publishing{Body: []byte("ping")}); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
		msg, got, err := ch.Get(q.Name, true)
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
	opts := mqtt.NewClientOptions().AddBroker("tcp://" + target).
		SetClientID("plug-e2e-go").SetConnectTimeout(5 * time.Second)
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
	if t := c.Subscribe("plug/e2e", 0, nil); t.WaitTimeout(5*time.Second) && t.Error() != nil {
		die("mqtt subscribe: %v", t.Error())
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
	// WithNoProxy → raw TCP (HTTP/2), so this exercises the seccomp supervisor.
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
