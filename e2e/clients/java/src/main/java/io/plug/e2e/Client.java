// The Java e2e client: reach a cluster service BY NAME, by protocol, using the
// language's NATURAL driver. Invoked (under plug) as:
//     java -jar /app/client.jar <proto> <host:port>
//
// http goes through java.net.http.HttpClient; every other protocol is a raw-TCP
// driver. plug sets JAVA_TOOL_OPTIONS=-DsocksProxyHost=... -DsocksProxyPort=...
// so the JVM routes all TCP through plug's SOCKS proxy automatically — this code
// does NOT configure any proxy, it just connects to <host:port> by name.
//
// Prints "E2E-OK <proto> — …" and exits 0 on success, or
// "E2E-FAIL <proto> <error>" and exits 1 on failure. A bash harness greps these.
package io.plug.e2e;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;
import java.time.Duration;
import java.util.Properties;
import java.util.concurrent.TimeUnit;

import com.mongodb.client.MongoClient;
import com.mongodb.client.MongoClients;
import com.mongodb.client.MongoDatabase;
import com.rabbitmq.client.Channel;
import com.rabbitmq.client.ConnectionFactory;
import com.rabbitmq.client.GetResponse;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.health.v1.HealthCheckRequest;
import io.grpc.health.v1.HealthCheckResponse;
import io.grpc.health.v1.HealthCheckResponse.ServingStatus;
import io.grpc.health.v1.HealthGrpc;
import org.bson.Document;
import org.eclipse.paho.client.mqttv3.MqttClient;
import org.eclipse.paho.client.mqttv3.MqttConnectOptions;
import org.eclipse.paho.client.mqttv3.MqttMessage;
import redis.clients.jedis.Jedis;

public final class Client {

    private static final int RETRIES = 12;
    private static final Duration TIMEOUT = Duration.ofSeconds(5);
    private static final int TIMEOUT_MS = 5000;

    public static void main(String[] args) {
        if (args.length < 2) {
            die("usage", "java -jar client.jar <proto> <host:port>");
        }
        String proto = args[0];
        String target = args[1];
        try {
            switch (proto) {
                case "http" -> doHttp(target);
                case "postgres" -> doPostgres(target);
                case "redis" -> doRedis(target);
                case "mongo" -> doMongo(target);
                case "amqp" -> doAmqp(target);
                case "mqtt" -> doMqtt(target);
                case "grpc" -> doGrpc(target);
                default -> die(proto, "proto not implemented in the java client");
            }
        } catch (Throwable t) {
            // Belt-and-suspenders: anything that escaped a doX becomes a FAIL.
            die(proto, describe(t));
        }
    }

    // --- output contract ---------------------------------------------------

    private static void ok(String proto, String detail) {
        System.out.println("E2E-OK " + proto + " — " + detail);
        System.exit(0);
    }

    private static void die(String proto, String error) {
        System.out.println("E2E-FAIL " + proto + " " + error);
        System.exit(1);
    }

    private static String describe(Throwable t) {
        String m = t.getMessage();
        return (m == null || m.isEmpty())
                ? t.getClass().getSimpleName()
                : t.getClass().getSimpleName() + ": " + m;
    }

    // --- retry helper ------------------------------------------------------

    @FunctionalInterface
    private interface Attempt<T> {
        T run() throws Exception;
    }

    // retry runs f up to ~12s so a service still warming behind the tunnel
    // doesn't flake the case.
    private static <T> T retry(Attempt<T> f) throws Exception {
        Exception last = null;
        for (int i = 0; i < RETRIES; i++) {
            try {
                return f.run();
            } catch (Exception e) {
                last = e;
                try {
                    TimeUnit.SECONDS.sleep(1);
                } catch (InterruptedException ie) {
                    Thread.currentThread().interrupt();
                    throw ie;
                }
            }
        }
        throw last != null ? last : new IllegalStateException("no attempt made");
    }

    // --- protocols ---------------------------------------------------------

    private static void doHttp(String target) throws Exception {
        String url = "http://" + target + "/get";
        HttpClient http = HttpClient.newBuilder().connectTimeout(TIMEOUT).build();
        HttpRequest req = HttpRequest.newBuilder(URI.create(url))
                .timeout(TIMEOUT).GET().build();
        HttpResponse<byte[]> resp = retry(() -> {
            HttpResponse<byte[]> r = http.send(req, HttpResponse.BodyHandlers.ofByteArray());
            if (r.statusCode() != 200) {
                throw new IllegalStateException("status " + r.statusCode());
            }
            return r;
        });
        ok("http", url + " → 200 (" + resp.body().length + " bytes)");
    }

    private static void doPostgres(String target) throws Exception {
        String jdbc = "jdbc:postgresql://" + target + "/plug";
        Properties props = new Properties();
        props.setProperty("user", "plug");
        props.setProperty("password", "plug");
        props.setProperty("connectTimeout", "5");   // seconds
        props.setProperty("socketTimeout", "5");    // seconds
        int n = retry(() -> {
            try (Connection c = DriverManager.getConnection(jdbc, props);
                 Statement st = c.createStatement();
                 ResultSet rs = st.executeQuery("SELECT 1")) {
                if (!rs.next()) {
                    throw new IllegalStateException("SELECT 1 returned no row");
                }
                return rs.getInt(1);
            }
        });
        if (n != 1) {
            die("postgres", "SELECT 1 = " + n);
        }
        ok("postgres", target + " → SELECT 1 = 1");
    }

    private static void doRedis(String target) throws Exception {
        int colon = target.lastIndexOf(':');
        String host = colon >= 0 ? target.substring(0, colon) : target;
        int port = colon >= 0 ? Integer.parseInt(target.substring(colon + 1)) : 6379;
        String pong = retry(() -> {
            try (Jedis jedis = new Jedis(host, port, TIMEOUT_MS)) {
                return jedis.ping();
            }
        });
        ok("redis", target + " → PING = " + pong);
    }

    private static void doMongo(String target) throws Exception {
        String uri = "mongodb://" + target
                + "/?directConnection=true&serverSelectionTimeoutMS=5000&connectTimeoutMS=5000";
        Document result = retry(() -> {
            try (MongoClient cli = MongoClients.create(uri)) {
                MongoDatabase admin = cli.getDatabase("admin");
                return admin.runCommand(new Document("ping", 1));
            }
        });
        double okv = result.get("ok", Number.class).doubleValue();
        if (okv != 1.0) {
            die("mongo", "ping ok = " + okv);
        }
        ok("mongo", target + " → ping ok:1");
    }

    private static void doAmqp(String target) throws Exception {
        int colon = target.lastIndexOf(':');
        String host = colon >= 0 ? target.substring(0, colon) : target;
        int port = colon >= 0 ? Integer.parseInt(target.substring(colon + 1)) : 5672;
        String queue = "plug-e2e";
        String body = retry(() -> {
            ConnectionFactory factory = new ConnectionFactory();
            factory.setHost(host);
            factory.setPort(port);
            factory.setUsername("plug");
            factory.setPassword("plug");
            factory.setConnectionTimeout(TIMEOUT_MS);
            try (var conn = factory.newConnection();
                 Channel ch = conn.createChannel()) {
                // "plug-e2e" is pre-declared by the broker's definitions.json — no
                // runtime queueDeclare (RabbitMQ 4 rejects some runtime declares).
                // default exchange, routingKey = queue name
                ch.basicPublish("", queue, null, "ping".getBytes(java.nio.charset.StandardCharsets.UTF_8));
                Thread.sleep(200);
                GetResponse msg = ch.basicGet(queue, true);
                if (msg == null) {
                    throw new IllegalStateException("no message back");
                }
                return new String(msg.getBody(), java.nio.charset.StandardCharsets.UTF_8);
            }
        });
        if (!"ping".equals(body)) {
            die("amqp", "got " + body);
        }
        ok("amqp", target + " → publish/get \"" + body + "\"");
    }

    private static void doMqtt(String target) throws Exception {
        String broker = "tcp://" + target;
        String topic = "plug/e2e";
        String payload = retry(() -> {
            java.util.concurrent.BlockingQueue<String> recv =
                    new java.util.concurrent.ArrayBlockingQueue<>(1);
            MqttClient client = new MqttClient(broker,
                    "plug-e2e-java-" + System.nanoTime(),
                    new org.eclipse.paho.client.mqttv3.persist.MemoryPersistence());
            MqttConnectOptions opts = new MqttConnectOptions();
            opts.setCleanSession(true);
            opts.setConnectionTimeout(5); // seconds
            try {
                client.connect(opts);
                client.subscribe(topic, 0, (t, m) -> recv.offer(new String(m.getPayload(),
                        java.nio.charset.StandardCharsets.UTF_8)));
                MqttMessage m = new MqttMessage("ping".getBytes(java.nio.charset.StandardCharsets.UTF_8));
                m.setQos(0);
                client.publish(topic, m);
                String got = recv.poll(6, TimeUnit.SECONDS);
                if (got == null) {
                    throw new IllegalStateException("no message received");
                }
                return got;
            } finally {
                try {
                    if (client.isConnected()) {
                        client.disconnect();
                    }
                    client.close();
                } catch (Exception ignore) {
                    // best effort cleanup
                }
            }
        });
        if (!"ping".equals(payload)) {
            die("mqtt", "got " + payload);
        }
        ok("mqtt", target + " → pub/sub \"" + payload + "\"");
    }

    private static void doGrpc(String target) throws Exception {
        ManagedChannel channel = ManagedChannelBuilder.forTarget(target)
                .usePlaintext().build();
        try {
            ServingStatus status = retry(() -> {
                HealthGrpc.HealthBlockingStub stub = HealthGrpc.newBlockingStub(channel)
                        .withDeadlineAfter(5, TimeUnit.SECONDS);
                HealthCheckResponse resp = stub.check(
                        HealthCheckRequest.newBuilder().setService("").build());
                return resp.getStatus();
            });
            if (status != ServingStatus.SERVING) {
                die("grpc", "status " + status);
            }
            ok("grpc", target + " → health SERVING");
        } finally {
            channel.shutdownNow();
            try {
                channel.awaitTermination(2, TimeUnit.SECONDS);
            } catch (InterruptedException ie) {
                Thread.currentThread().interrupt();
            }
        }
    }

    private Client() {
    }
}
