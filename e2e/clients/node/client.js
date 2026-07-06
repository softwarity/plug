// The Node.js e2e client: reach a cluster service BY NAME, by protocol, using
// the language's NATURAL driver. Invoked (under plug) as:
//   node /app/client.js <proto> <host:port>
//
// http goes through Node's built-in `http` module (http.get resolves through
// libc, which plug's hook covers — do NOT use fetch/undici, it ignores proxy
// env); every other protocol is a raw-TCP driver → it exercises the seccomp
// supervisor. Prints "E2E-OK <proto> — …" and exits 0 on success, otherwise
// prints "E2E-FAIL <proto> <error>" and exits non-zero.

'use strict';

const http = require('http');

// ---- output contract --------------------------------------------------------

function ok(proto, detail) {
  console.log('E2E-OK ' + proto + ' — ' + detail);
  process.exit(0);
}

function die(proto, err) {
  const msg = err && err.stack ? (err.message || String(err)) : String(err);
  console.error('E2E-FAIL ' + proto + ' ' + msg);
  process.exit(1);
}

// retry runs an async fn up to ~12 times spaced 1s so a service still warming
// behind the tunnel doesn't flake the case.
async function retry(fn) {
  let lastErr;
  for (let i = 0; i < 12; i++) {
    try {
      return await fn();
    } catch (err) {
      lastErr = err;
      await sleep(1000);
    }
  }
  throw lastErr;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ---- http -------------------------------------------------------------------

async function doHTTP(target) {
  const url = 'http://' + target + '/get';
  const bytes = await retry(
    () =>
      new Promise((resolve, reject) => {
        const req = http.get(url, { timeout: 5000 }, (res) => {
          if (res.statusCode !== 200) {
            res.resume();
            reject(new Error('status ' + res.statusCode));
            return;
          }
          let len = 0;
          res.on('data', (chunk) => {
            len += chunk.length;
          });
          res.on('end', () => resolve(len));
          res.on('error', reject);
        });
        req.on('timeout', () => req.destroy(new Error('timeout')));
        req.on('error', reject);
      })
  );
  ok('http', url + ' → 200 (' + bytes + ' bytes)');
}

// ---- postgres ---------------------------------------------------------------

async function doPostgres(target) {
  const { Client } = require('pg');
  const [host, port] = splitTarget(target, 5432);
  const result = await retry(async () => {
    const client = new Client({
      host,
      port,
      user: 'plug',
      password: 'plug',
      database: 'plug',
      ssl: false,
      connectionTimeoutMillis: 5000,
      query_timeout: 5000,
    });
    try {
      await client.connect();
      const res = await client.query('SELECT 1');
      return res.rows[0]['?column?'];
    } finally {
      await client.end().catch(() => {});
    }
  });
  if (Number(result) !== 1) {
    return die('postgres', new Error('SELECT 1 = ' + result));
  }
  ok('postgres', target + ' → SELECT 1 = 1');
}

// ---- redis ------------------------------------------------------------------

async function doRedis(target) {
  const { createClient } = require('redis');
  const pong = await retry(async () => {
    const client = createClient({
      url: 'redis://' + target,
      socket: { connectTimeout: 5000, reconnectStrategy: false },
    });
    // Swallow error events so a failed attempt rejects the connect() promise
    // instead of crashing the process.
    client.on('error', () => {});
    try {
      await client.connect();
      return await client.ping();
    } finally {
      await client.quit().catch(() => {});
    }
  });
  ok('redis', target + ' → PING = ' + pong);
}

// ---- mongo ------------------------------------------------------------------

async function doMongo(target) {
  const { MongoClient } = require('mongodb');
  const uri =
    'mongodb://' + target + '/?directConnection=true&serverSelectionTimeoutMS=5000';
  const res = await retry(async () => {
    const client = new MongoClient(uri, {
      serverSelectionTimeoutMS: 5000,
      connectTimeoutMS: 5000,
    });
    try {
      await client.connect();
      return await client.db('admin').command({ ping: 1 });
    } finally {
      await client.close().catch(() => {});
    }
  });
  if (!res || res.ok !== 1) {
    return die('mongo', new Error('ping ok=' + (res && res.ok)));
  }
  ok('mongo', target + ' → ping ok');
}

// ---- amqp -------------------------------------------------------------------

async function doAMQP(target) {
  const amqp = require('amqplib');
  const url = 'amqp://plug:plug@' + target + '/';
  const body = await retry(async () => {
    let conn;
    let ch;
    try {
      conn = await amqp.connect(url, { timeout: 5000 });
      ch = await conn.createChannel();
      // "plug-e2e" is pre-declared by the broker's definitions.json — no runtime
      // assertQueue (RabbitMQ 4 rejects some runtime declares).
      ch.sendToQueue('plug-e2e', Buffer.from('ping'));
      await sleep(200);
      const msg = await ch.get('plug-e2e', { noAck: true });
      if (!msg) {
        throw new Error('no message back');
      }
      return msg.content.toString();
    } finally {
      if (ch) await ch.close().catch(() => {});
      if (conn) await conn.close().catch(() => {});
    }
  });
  if (body !== 'ping') {
    return die('amqp', new Error('body ' + JSON.stringify(body)));
  }
  ok('amqp', target + ' → publish/get "' + body + '"');
}

// ---- mqtt -------------------------------------------------------------------

async function doMQTT(target) {
  const mqtt = require('mqtt');
  const payload = await retry(
    () =>
      new Promise((resolve, reject) => {
        const client = mqtt.connect('mqtt://' + target, {
          connectTimeout: 5000,
          reconnectPeriod: 0,
          clientId: 'plug-e2e-node-' + Math.random().toString(16).slice(2, 10),
        });
        let done = false;
        const finish = (err, value) => {
          if (done) return;
          done = true;
          clearTimeout(timer);
          client.end(true, () => (err ? reject(err) : resolve(value)));
        };
        const timer = setTimeout(
          () => finish(new Error('no message received')),
          6000
        );
        client.on('error', (err) => finish(err));
        client.on('message', (_topic, message) => finish(null, message.toString()));
        client.on('connect', () => {
          client.subscribe('plug/e2e', { qos: 0 }, (err) => {
            if (err) return finish(err);
            client.publish('plug/e2e', 'ping', { qos: 0 });
          });
        });
      })
  );
  if (payload !== 'ping') {
    return die('mqtt', new Error('payload ' + JSON.stringify(payload)));
  }
  ok('mqtt', target + ' → pub/sub "' + payload + '"');
}

// ---- grpc -------------------------------------------------------------------

async function doGRPC(target) {
  const grpc = require('@grpc/grpc-js');
  const protoLoader = require('@grpc/proto-loader');
  const path = require('path');

  const def = protoLoader.loadSync(path.join(__dirname, 'health.proto'), {
    keepCase: true,
    longs: String,
    enums: String,
    defaults: true,
    oneofs: true,
  });
  const proto = grpc.loadPackageDefinition(def);
  const Health = proto.grpc.health.v1.Health;
  const client = new Health(target, grpc.credentials.createInsecure());

  const status = await retry(
    () =>
      new Promise((resolve, reject) => {
        const deadline = new Date(Date.now() + 5000);
        client.Check({ service: '' }, { deadline }, (err, resp) => {
          if (err) return reject(err);
          resolve(resp.status);
        });
      })
  );
  try {
    client.close();
  } catch (_) {
    // ignore
  }
  // SERVING is enum value 1; with enums:String proto-loader yields "SERVING".
  if (status !== 'SERVING' && status !== 1) {
    return die('grpc', new Error('status ' + status));
  }
  ok('grpc', target + ' → health SERVING');
}

// ---- websocket --------------------------------------------------------------

async function doWebSocket(target) {
  const WebSocket = require('ws');
  const url = 'ws://' + target + '/';
  const msg = 'plug-e2e-ws-42';
  const got = await retry(
    () =>
      new Promise((resolve, reject) => {
        const socket = new WebSocket(url, { handshakeTimeout: 5000 });
        const timer = setTimeout(() => {
          socket.terminate();
          reject(new Error('timeout'));
        }, 6000);
        socket.on('open', () => socket.send(msg));
        socket.on('message', (data) => {
          clearTimeout(timer);
          socket.close();
          resolve(data.toString());
        });
        socket.on('error', (err) => {
          clearTimeout(timer);
          reject(err);
        });
      })
  );
  if (got !== msg) {
    return die('websocket', new Error('echo mismatch: ' + JSON.stringify(got)));
  }
  ok('websocket', url + ' → echo "' + got + '"');
}

// ---- helpers ----------------------------------------------------------------

// splitTarget parses "host:port" (IPv6-naive, matches the e2e host:port form).
function splitTarget(target, defaultPort) {
  const idx = target.lastIndexOf(':');
  if (idx === -1) {
    return [target, defaultPort];
  }
  const host = target.slice(0, idx);
  const port = parseInt(target.slice(idx + 1), 10) || defaultPort;
  return [host, port];
}

// ---- main -------------------------------------------------------------------

async function main() {
  const proto = process.argv[2];
  const target = process.argv[3];
  if (!proto || !target) {
    die('usage', new Error('node client.js <proto> <host:port>'));
    return;
  }
  const handlers = {
    http: doHTTP,
    postgres: doPostgres,
    redis: doRedis,
    mongo: doMongo,
    amqp: doAMQP,
    mqtt: doMQTT,
    grpc: doGRPC,
    websocket: doWebSocket,
  };
  const handler = handlers[proto];
  if (!handler) {
    return die(proto, new Error('proto not implemented in the node client'));
  }
  try {
    await handler(target);
  } catch (err) {
    die(proto, err);
  }
}

main();
