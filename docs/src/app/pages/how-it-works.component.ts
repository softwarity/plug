import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';

@Component({
  selector: 'app-how-it-works',
  imports: [RouterLink],
  preserveWhitespaces: true,
  styles: [
    `
      .diagram {
        margin: 20px 0 8px;
        padding: 8px 4px;
        overflow-x: auto;
      }
      .diagram svg {
        display: block;
        width: 100%;
        min-width: 620px;
        max-width: 900px;
        height: auto;
        margin: 0 auto;
      }
    `,
  ],
  template: `
    <h2>How it works</h2>

    <p>
      <strong>One mechanism: a userspace TUN, over an SSH tunnel.</strong> plug captures your
      command's cluster traffic at the <strong>IP layer</strong> and splices it, by name, to a tiny
      agent (Alpine, one static Go binary) running in the cluster. Your app's socket is never touched,
      and nothing but a private, reserved IP range is ever intercepted.
    </p>

    <div class="diagram" role="img" aria-label="plug's datapath runs both ways over one SSH connection: outbound, your process reaches cluster services by name; inbound, a name you serve with -s is reachable from inside the cluster.">
      <svg viewBox="0 0 820 400" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system, Segoe UI, Roboto, sans-serif">
        <defs>
          <marker id="hiw-out" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="#a371f7" /></marker>
          <marker id="hiw-in" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="#3fb950" /></marker>
        </defs>
        <rect x="2" y="2" width="816" height="396" rx="16" fill="#0d1117" stroke="#30363d" stroke-width="1.5" />

        <rect x="30" y="70" width="234" height="264" rx="12" fill="#0e141b" stroke="#30363d" />
        <text x="50" y="98" font-size="12" letter-spacing="2" fill="#8b949e" font-weight="700">YOUR MACHINE</text>
        <rect x="50" y="150" width="194" height="104" rx="9" fill="#21262d" stroke="#30363d" />
        <text x="147" y="182" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="14" fill="#e6edf3" font-weight="600">your process</text>
        <text x="147" y="203" text-anchor="middle" font-size="11" fill="#8b949e">npm run start:dev</text>
        <text x="147" y="234" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="10" fill="#6e7681">plug -s service1:80:3000</text>

        <rect x="286" y="150" width="248" height="104" rx="10" fill="#0e141b" stroke="#30363d" />
        <text x="410" y="140" text-anchor="middle" font-size="10.5" fill="#8b949e">one SSH connection · userspace TUN · DNS by name</text>

        <rect x="556" y="70" width="234" height="264" rx="12" fill="#0e141b" stroke="#30363d" />
        <text x="576" y="98" font-size="12" letter-spacing="2" fill="#8b949e" font-weight="700">THE CLUSTER</text>
        <ellipse cx="628" cy="168" rx="30" ry="9" fill="#161b22" stroke="#30363d" />
        <path d="M598 168 V204 a30 9 0 0 0 60 0 V168" fill="#161b22" stroke="#30363d" />
        <ellipse cx="628" cy="168" rx="30" ry="9" fill="#161b22" stroke="#30363d" />
        <text x="628" y="192" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="13" fill="#e6edf3" font-weight="600">db</text>
        <rect x="690" y="150" width="84" height="40" rx="7" fill="#161b22" stroke="#a371f7" stroke-width="1.3" />
        <text x="732" y="175" text-anchor="middle" font-size="11" fill="#e6edf3">agent</text>
        <rect x="596" y="228" width="150" height="34" rx="7" fill="#161b22" stroke="#30363d" stroke-dasharray="4 3" />
        <text x="671" y="250" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="11" fill="#3fb950">service1</text>

        <line x1="244" y1="176" x2="596" y2="176" stroke="#a371f7" stroke-width="2.3" marker-end="url(#hiw-out)" />
        <text x="410" y="169" text-anchor="middle" font-size="10.5" fill="#a371f7" font-weight="600">outbound - reach db by name</text>
        <text x="360" y="199" text-anchor="middle" font-size="9.5" fill="#6e7681">name → fake IP → SSH channel → agent dials db</text>

        <!--
          The five #3fb950 below are SVG PRESENTATION ATTRIBUTES, left as
          literals on purpose. var() in an attribute is resolved inconsistently
          across browsers, unlike in a stylesheet, and a diagram that loses its
          colour on one of them is worse than a colour written twice. The token
          in styles.scss carries the same value; if it moves, these move with it.
        -->
        <line x1="596" y1="228" x2="244" y2="228" stroke="#3fb950" stroke-width="2.3" marker-end="url(#hiw-in)" />
        <text x="410" y="245" text-anchor="middle" font-size="10.5" fill="#3fb950" font-weight="600">inbound - -s publishes service1</text>
        <text x="410" y="286" text-anchor="middle" font-size="9.5" fill="#6e7681">a cluster workload hits service1 → SSH remote-forward → your local :3000</text>

        <line x1="70" y1="360" x2="100" y2="360" stroke="#a371f7" stroke-width="2.6" />
        <text x="108" y="364" font-size="11.5" fill="#8b949e">outbound - SSH direct-tcpip (agent opens the real connection)</text>
        <line x1="500" y1="360" x2="530" y2="360" stroke="#3fb950" stroke-width="2.6" />
        <text x="538" y="364" font-size="11.5" fill="#8b949e">inbound - SSH remote-forward (-s)</text>
      </svg>
    </div>

    <h3>The data path, step by step</h3>
    <ol>
      <li>
        <strong>Your command connects by name.</strong> It asks for
        <code>my-service:8080</code> exactly as it would inside the cluster - no proxy variables, no
        rewritten URL.
      </li>
      <li>
        <strong>DNS is answered in-stack.</strong> plug runs a
        <a href="https://gvisor.dev/" target="_blank" rel="noopener">gVisor</a> userspace network
        stack behind a TUN device (<code>/dev/net/tun</code> on Linux, <code>utun</code> on macOS,
        WinTUN on Windows). A single-label name is handed a private stand-in IP from a reserved
        range (<code>198.18.x.x</code>), and the OS routes that range straight into the TUN.
      </li>
      <li>
        <strong>plug reads the packet and splices the flow.</strong> The <code>connect()</code>
        surfaces as a packet in plug's stack; because plug minted the stand-in IP, it knows the real
        name, terminates the flow, and splices it onto the single SSH tunnel - carrying the
        <em>name</em>, not an IP.
      </li>
      <li>
        <strong>The agent dials from inside.</strong> The agent opens an SSH
        <code>direct-tcpip</code> channel: it resolves the name with the cluster's own resolver and
        connects to <code>service:port</code> from inside the cluster. Standard SSH, so any client
        speaks it - the server is ours, a few hundred lines over
        <code>golang.org/x/crypto/ssh</code>, with no shell and no privileged helper behind it.
      </li>
    </ol>

    <h3>Why capture at the IP layer</h3>
    <ul>
      <li>
        <strong>Every runtime, no config.</strong> The app's socket is never touched, so it covers
        Node, the JVM (Spring/Quarkus/Netty), Python, Ruby, PHP, curl - and, crucially,
        <strong>Go and other statically-linked binaries, and gRPC</strong>, the exact cases an
        <code>LD_PRELOAD</code>/proxy approach cannot reach.
      </li>
      <li>
        <strong>Split-horizon by name shape.</strong> Single-label names (<code>my-service</code>,
        <code>rabbitmq</code>) go to the cluster; dotted FQDNs (<code>api.github.com</code>) and
        <code>localhost</code> resolve and connect <strong>directly</strong>, so your app keeps
        normal internet access.
      </li>
      <li>
        <strong>Self-healing transport.</strong> A keepalive keeps the SSH tunnel warm and a drop
        (an idle NAT/VPN timeout, a laptop sleep, an agent restart) is re-dialed transparently, so
        long sessions never need a restart. The agent's host key is pinned on first use.
      </li>
    </ul>

    <h3>The privilege, granted once</h3>
    <p>
      Creating the TUN, setting routes and repointing DNS needs privilege - granted
      <strong>once at install</strong> so day-to-day <code>plug &lt;cmd&gt;</code> needs none. Each OS
      does it the native way:
    </p>
    <ul>
      <li>
        <strong>Linux</strong> - file capabilities (<code>cap_net_admin</code>,
        <code>cap_sys_admin</code>, <code>cap_net_bind_service</code>). Each launch also gets a
        private resolver in its own mount namespace, which is what lets several clusters run side by
        side for free.
      </li>
      <li>
        <strong>macOS</strong> - a setuid-root helper holds the TUN + DNS, then drops your command
        back to your user. Because macOS repoints DNS machine-wide, the data path lives in a small
        per-cluster <strong>daemon</strong> that survives each run and restores your DNS ~30 s after
        the last <code>plug</code> of that cluster exits (<code>plug down</code> stops it now).
      </li>
      <li>
        <strong>Windows</strong> - the data path lives in a <strong>SYSTEM service</strong> installed
        once (elevated); every <code>plug</code> run is a non-elevated launcher that starts it on
        demand and delegates to it, so the command itself never needs admin.
      </li>
    </ul>

    <h3>Several clusters at once</h3>
    <p>
      Two different clusters can run side by side - <code>plug -p a</code> and
      <code>plug -p b</code> - with the same service names resolving to the right backend each time.
      How plug keeps them apart depends on the OS:
    </p>
    <ul>
      <li><strong>Linux</strong> gives every launch a private resolver in its own mount namespace, so two launches never share DNS - isolation for free.</li>
      <li><strong>Windows</strong> repoints the system resolver machine-wide, so the SYSTEM service holds one tunnel per cluster and disambiguates each flow <strong>at <code>connect()</code></strong>: the source port maps to the owning process, whose ancestry is walked back to the <code>plug -p x</code> that launched it - that is its cluster (PID-at-connect).</li>
      <li><strong>macOS</strong> - the global daemon holds one tunnel per cluster and attributes each flow by PID at connect, like Windows (proven simultaneously in CI).</li>
    </ul>
    <p>
      One honest limit: if a process <strong>fully detaches</strong> from the <code>plug</code> that
      launched it - a rare case, where it re-parents to the system and the ancestry link is lost -
      plug can no longer tell which cluster it belongs to, so it <strong>declines</strong> that
      connection rather than risk routing it to the wrong cluster.
    </p>

    <h3>The reverse direction</h3>
    <p>
      The transport carries the other way too: <code>plug -s
      &lt;name&gt;:&lt;cluster-port&gt;:&lt;local-port&gt; &lt;cmd&gt;</code> opens a
      <strong>dedicated SSH connection for the session</strong> and asks the agent for a standard
      <strong>remote forward</strong> - the agent listens on the
      cluster port, and every connection a cluster workload makes rides that connection back to your
      local port (dedicated, so the port's lifetime is exactly the session's, even where the forward
      datapath lives in a shared daemon). The cluster <em>name</em> that points at it is
      <strong>provisioned by the agent on the fly</strong>: a signpost container carrying the DNS
      alias (Docker, via the socket) or a Service whose endpoints name the agent pod holding the
      session (Kubernetes, via a Services-and-their-endpoints role) - created on <code>-s</code>, removed when the session ends, and
      re-provisioned automatically after a reconnect, with no stack redeploy. plug verifies the whole
      loop at startup through the cluster's own DNS. A <strong>deployed</strong> service owning the
      name is <strong>parked</strong> for the session (containers stopped / Swarm scaled to 0 / k8s
      Service repointed) and <strong>restored on exit</strong> - your process substitutes for it; a
      name held by another live plug session is refused, and the refusal
      <a routerLink="/troubleshooting">names the process holding it</a>. That claim is the agent's,
      not the signpost's: it leases the name to the session serving it, so the name stays that
      session's even in the moments no signpost exists - right after an agent restart, for
      instance. A session is only ever held to be alive while its own forward still answers, so
      nothing stays locked after a session dies.
      See <a routerLink="/swarm">Swarm</a> and
      <a routerLink="/kubernetes">Kubernetes</a>.
    </p>

    <h3>Built with open source</h3>
    <p>plug stands on the shoulders of these projects - thank you:</p>
    <table>
      <thead>
        <tr><th scope="col">Dependency</th><th scope="col">Role</th><th scope="col">License</th></tr>
      </thead>
      <tbody>
        <tr><td><a href="https://pkg.go.dev/golang.org/x/crypto/ssh" target="_blank" rel="noopener">golang.org/x/crypto/ssh</a></td><td>The transport, on both ends: the CLI's in-process client, and the agent's server doing the <code>direct-tcpip</code> dials and the remote forwards</td><td>BSD</td></tr>
        <tr><td><a href="https://github.com/WireGuard/wireguard-go" target="_blank" rel="noopener">wireguard-go</a> + <a href="https://gvisor.dev/" target="_blank" rel="noopener">gVisor</a></td><td>The userspace TUN device and network stack that answer DNS and terminate flows in-process</td><td>MIT · Apache-2.0</td></tr>
        <tr><td><a href="https://go.dev/" target="_blank" rel="noopener">Go</a></td><td>The CLI - one static binary per platform, no runtime dependencies</td><td>BSD</td></tr>
        <tr><td><a href="https://www.alpinelinux.org/" target="_blank" rel="noopener">Alpine Linux</a></td><td>Base of the agent image - one static Go binary + the binaries it serves</td><td>MIT</td></tr>
      </tbody>
    </table>
  `,
})
export class HowItWorksComponent {}
