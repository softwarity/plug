#!/usr/bin/env python3
# The Python e2e client: reach a cluster service BY NAME, by protocol, using the
# language's NATURAL driver. Invoked (under plug) as:  python client.py <proto> <host:port>
#
# http goes through requests / urllib3 (honors HTTP_PROXY -> exercises the proxy
# path); every other protocol is a raw-TCP driver (no proxy honoring) -> it
# exercises the seccomp supervisor. Prints "E2E-OK <proto> - ..." and exits 0 on
# success, "E2E-FAIL <proto> <error>" and exits 1 on failure.
import sys
import time


def die(proto, error):
    print(f"E2E-FAIL {proto} {error}")
    sys.exit(1)


def ok(proto, detail):
    print(f"E2E-OK {proto} - {detail}")
    sys.exit(0)


def retry(f):
    """Run f up to ~12s so a service still warming behind the tunnel doesn't
    flake the case. Returns (value, None) on success or (None, last_error)."""
    err = None
    for _ in range(12):
        try:
            return f(), None
        except Exception as e:  # noqa: BLE001 - retry any transient failure
            err = e
            time.sleep(1)
    return None, err


def do_http(target):
    import requests

    url = "http://" + target + "/get"

    def attempt():
        resp = requests.get(url, timeout=5)
        if resp.status_code != 200:
            raise RuntimeError(f"status {resp.status_code}")
        return resp.content

    body, err = retry(attempt)
    if err is not None:
        die("http", f"GET {url}: {err}")
    ok("http", f"{url} -> 200 ({len(body)} bytes)")


def do_postgres(target):
    import psycopg2

    host, _, port = target.partition(":")

    def attempt():
        conn = psycopg2.connect(
            host=host,
            port=int(port) if port else 5432,
            user="plug",
            password="plug",
            dbname="plug",
            sslmode="disable",
            connect_timeout=5,
        )
        try:
            cur = conn.cursor()
            cur.execute("SELECT 1")
            row = cur.fetchone()
            return row[0]
        finally:
            conn.close()

    n, err = retry(attempt)
    if err is not None:
        die("postgres", f"SELECT 1 ({target}): {err}")
    if n != 1:
        die("postgres", f"SELECT 1 = {n}")
    ok("postgres", f"{target} -> SELECT 1 = 1")


def do_redis(target):
    import redis

    host, _, port = target.partition(":")

    def attempt():
        rdb = redis.Redis(
            host=host,
            port=int(port) if port else 6379,
            socket_connect_timeout=5,
            socket_timeout=5,
        )
        try:
            return rdb.ping()
        finally:
            rdb.close()

    pong, err = retry(attempt)
    if err is not None:
        die("redis", f"PING ({target}): {err}")
    if not pong:
        die("redis", f"PING = {pong}")
    ok("redis", f"{target} -> PING = {pong}")


def do_mongo(target):
    from pymongo import MongoClient

    uri = "mongodb://" + target + "/?directConnection=true"

    def attempt():
        cli = MongoClient(uri, serverSelectionTimeoutMS=5000)
        try:
            return cli.admin.command("ping")
        finally:
            cli.close()

    res, err = retry(attempt)
    if err is not None:
        die("mongo", f"ping ({target}): {err}")
    if not res or res.get("ok") != 1.0:
        die("mongo", f"ping ok = {res}")
    ok("mongo", f"{target} -> ping ok")


def do_amqp(target):
    import pika

    def attempt():
        params = pika.URLParameters("amqp://plug:plug@" + target + "/")
        params.socket_timeout = 5
        params.blocked_connection_timeout = 5
        conn = pika.BlockingConnection(params)
        try:
            ch = conn.channel()
            # "plug-e2e" is pre-declared by the broker's definitions.json — no
            # runtime queue_declare (RabbitMQ 4 rejects some runtime declares).
            ch.basic_publish(exchange="", routing_key="plug-e2e", body=b"ping")
            time.sleep(0.2)
            method, _props, body = ch.basic_get(queue="plug-e2e", auto_ack=True)
            if method is None:
                raise RuntimeError("no message back")
            return body.decode() if isinstance(body, (bytes, bytearray)) else body
        finally:
            conn.close()

    body, err = retry(attempt)
    if err is not None:
        die("amqp", f"({target}): {err}")
    if body != "ping":
        die("amqp", f"got body {body!r}")
    ok("amqp", f"{target} -> publish/get {body!r}")


def do_mqtt(target):
    import paho.mqtt.client as mqtt

    host, _, port = target.partition(":")
    port = int(port) if port else 1883

    def attempt():
        received = []
        client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id="plug-e2e-py")

        def on_message(_client, _userdata, msg):
            received.append(msg.payload)

        client.on_message = on_message
        client.connect(host, port, keepalive=5)
        client.loop_start()
        try:
            client.subscribe("plug/e2e", qos=0)
            time.sleep(0.2)
            client.publish("plug/e2e", payload="ping", qos=0)
            deadline = time.time() + 6
            while not received and time.time() < deadline:
                time.sleep(0.05)
            if not received:
                raise RuntimeError("no message received")
            payload = received[0]
            return payload.decode() if isinstance(payload, (bytes, bytearray)) else payload
        finally:
            client.loop_stop()
            client.disconnect()

    payload, err = retry(attempt)
    if err is not None:
        die("mqtt", f"({target}): {err}")
    if payload != "ping":
        die("mqtt", f"got payload {payload!r}")
    ok("mqtt", f"{target} -> pub/sub {payload!r}")


def do_grpc(target):
    import grpc
    from grpc_health.v1 import health_pb2, health_pb2_grpc

    def attempt():
        channel = grpc.insecure_channel(target)
        try:
            stub = health_pb2_grpc.HealthStub(channel)
            resp = stub.Check(health_pb2.HealthCheckRequest(service=""), timeout=5)
            return resp.status
        finally:
            channel.close()

    status, err = retry(attempt)
    if err is not None:
        die("grpc", f"health ({target}): {err}")
    if status != health_pb2.HealthCheckResponse.SERVING:
        die("grpc", f"status {status}")
    ok("grpc", f"{target} -> health SERVING")


DISPATCH = {
    "http": do_http,
    "postgres": do_postgres,
    "redis": do_redis,
    "mongo": do_mongo,
    "amqp": do_amqp,
    "mqtt": do_mqtt,
    "grpc": do_grpc,
}


def main():
    if len(sys.argv) < 3:
        die("-", "usage: client.py <proto> <host:port>")
    proto, target = sys.argv[1], sys.argv[2]
    handler = DISPATCH.get(proto)
    if handler is None:
        die(proto, "proto not implemented in the python client")
    handler(target)


if __name__ == "__main__":
    main()
