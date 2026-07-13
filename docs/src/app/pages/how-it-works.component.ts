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
      agent (Alpine + <code>sshd</code>) running in the cluster. Your app's socket is never touched,
      and nothing but a private, reserved IP range is ever intercepted.
    </p>

    <div class="diagram" role="img" aria-label="plug data path: your command connects by name, DNS is answered in-stack, the flow is spliced over an SSH tunnel, and the cluster agent dials the service from inside.">
      <svg viewBox="0 0 900 500" xmlns="http://www.w3.org/2000/svg" font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif">
        <defs>
          <marker id="hiw-arw" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 L10 5 L0 10 z" fill="#8b949e"/>
          </marker>
          <marker id="hiw-arwBlue" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 L10 5 L0 10 z" fill="#58a6ff"/>
          </marker>
          <marker id="hiw-arwGreen" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 L10 5 L0 10 z" fill="#3fb950"/>
          </marker>
        </defs>

        <text x="48" y="44" fill="#8b949e" font-size="13" letter-spacing="2" font-weight="600">YOUR MACHINE</text>
        <rect x="40" y="58" width="404" height="404" rx="14" fill="#161b22" stroke="#30363d" stroke-width="1.5"/>

        <rect x="72" y="92" width="340" height="66" rx="9" fill="#21262d" stroke="#30363d" stroke-width="1.5"/>
        <text x="92" y="122" fill="#e6edf3" font-size="15" font-weight="600" font-family="ui-monospace, 'SF Mono', Menlo, monospace">plug npm run start:dev</text>
        <text x="92" y="142" fill="#8b949e" font-size="12.5">your process, its sockets never touched</text>

        <line x1="242" y1="158" x2="242" y2="196" stroke="#8b949e" stroke-width="1.6" marker-end="url(#hiw-arw)"/>
        <circle cx="266" cy="177" r="11" fill="#a371f7"/>
        <text x="266" y="181" fill="#fff" font-size="12.5" font-weight="700" text-anchor="middle">1</text>
        <text x="286" y="181" fill="#c9d1d9" font-size="12.5">connects by name</text>

        <rect x="72" y="200" width="340" height="228" rx="11" fill="rgba(163,113,247,0.07)" stroke="#a371f7" stroke-width="1.5"/>
        <text x="92" y="228" fill="#a371f7" font-size="13" font-weight="700" letter-spacing="0.5">plug — userspace data path</text>

        <rect x="92" y="242" width="300" height="70" rx="8" fill="#21262d" stroke="#30363d" stroke-width="1.2"/>
        <text x="108" y="266" fill="#e6edf3" font-size="13.5" font-weight="600">DNS answered in-stack</text>
        <text x="108" y="286" fill="#8b949e" font-size="12">the name gets a private stand-in address,</text>
        <text x="108" y="302" fill="#8b949e" font-size="12">which the OS routes straight into the tunnel</text>
        <circle cx="372" cy="258" r="10" fill="#a371f7"/>
        <text x="372" y="262" fill="#fff" font-size="11.5" font-weight="700" text-anchor="middle">2</text>

        <line x1="242" y1="312" x2="242" y2="340" stroke="#8b949e" stroke-width="1.6" marker-end="url(#hiw-arw)"/>

        <rect x="92" y="344" width="300" height="66" rx="8" fill="#21262d" stroke="#30363d" stroke-width="1.2"/>
        <text x="108" y="370" fill="#e6edf3" font-size="13.5" font-weight="600">TUN + userspace network stack</text>
        <text x="108" y="392" fill="#8b949e" font-size="12">recovers the name and splices the flow</text>
        <circle cx="372" cy="360" r="10" fill="#a371f7"/>
        <text x="372" y="364" fill="#fff" font-size="11.5" font-weight="700" text-anchor="middle">3</text>

        <line x1="444" y1="322" x2="612" y2="322" stroke="#58a6ff" stroke-width="2.4" stroke-dasharray="8 5" marker-end="url(#hiw-arwBlue)"/>
        <rect x="470" y="286" width="116" height="26" rx="13" fill="#161b22" stroke="#58a6ff" stroke-width="1.3"/>
        <text x="528" y="303" fill="#58a6ff" font-size="12.5" font-weight="600" text-anchor="middle">SSH tunnel</text>
        <text x="528" y="342" fill="#8b949e" font-size="11.5" text-anchor="middle">one flow, by name</text>
        <text x="528" y="357" fill="#8b949e" font-size="11.5" text-anchor="middle">port 2222</text>

        <text x="628" y="44" fill="#8b949e" font-size="13" letter-spacing="2" font-weight="600">CLUSTER</text>
        <rect x="612" y="58" width="248" height="404" rx="14" fill="#161b22" stroke="#30363d" stroke-width="1.5"/>

        <rect x="636" y="242" width="200" height="96" rx="10" fill="rgba(88,166,255,0.07)" stroke="#58a6ff" stroke-width="1.5"/>
        <text x="656" y="270" fill="#58a6ff" font-size="13.5" font-weight="700">plug agent (sshd)</text>
        <text x="656" y="292" fill="#8b949e" font-size="12">resolves the name and</text>
        <text x="656" y="308" fill="#8b949e" font-size="12">dials the service from</text>
        <text x="656" y="324" fill="#8b949e" font-size="12">inside the cluster</text>
        <circle cx="816" cy="258" r="10" fill="#58a6ff"/>
        <text x="816" y="262" fill="#fff" font-size="11.5" font-weight="700" text-anchor="middle">4</text>

        <line x1="736" y1="242" x2="736" y2="170" stroke="#3fb950" stroke-width="1.8" marker-end="url(#hiw-arwGreen)"/>

        <rect x="648" y="104" width="176" height="60" rx="9" fill="#21262d" stroke="#3fb950" stroke-width="1.5"/>
        <text x="736" y="130" fill="#e6edf3" font-size="14" font-weight="600" text-anchor="middle" font-family="ui-monospace, 'SF Mono', Menlo, monospace">my-service:8080</text>
        <text x="736" y="150" fill="#8b949e" font-size="12" text-anchor="middle">a normal cluster workload</text>

        <text x="48" y="490" fill="#8b949e" font-size="12">Names go to the cluster; dotted host names (api.github.com) and localhost stay on your machine.</text>
      </svg>
    </div>

    <h3>The data path, step by step</h3>
    <ol>
      <li>
        <strong>Your command connects by name.</strong> It asks for
        <code>my-service:8080</code> exactly as it would inside the cluster — no proxy variables, no
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
        name, terminates the flow, and splices it onto the single SSH tunnel — carrying the
        <em>name</em>, not an IP.
      </li>
      <li>
        <strong>The agent dials from inside.</strong> A stock <code>sshd</code> opens an SSH
        <code>direct-tcpip</code> channel: it resolves the name with the cluster's own resolver and
        connects to <code>service:port</code> from inside the cluster. No server code of ours.
      </li>
    </ol>

    <h3>Why capture at the IP layer</h3>
    <ul>
      <li>
        <strong>Every runtime, no config.</strong> The app's socket is never touched, so it covers
        Node, the JVM (Spring/Quarkus/Netty), Python, Ruby, PHP, curl — and, crucially,
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
      Creating the TUN, setting routes and repointing DNS needs privilege — granted
      <strong>once at install</strong> so day-to-day <code>plug &lt;cmd&gt;</code> needs none. Each OS
      does it the native way:
    </p>
    <ul>
      <li>
        <strong>Linux</strong> — file capabilities (<code>cap_net_admin</code>,
        <code>cap_sys_admin</code>, <code>cap_net_bind_service</code>). Each launch also gets a
        private resolver in its own mount namespace, which is what lets several clusters run side by
        side for free.
      </li>
      <li>
        <strong>macOS</strong> — a setuid-root helper holds the TUN + DNS, then drops your command
        back to your user. Because macOS repoints DNS machine-wide, the data path lives in a small
        per-cluster <strong>daemon</strong> that survives each run and restores your DNS ~30 s after
        the last <code>plug</code> of that cluster exits (<code>plug down</code> stops it now).
      </li>
      <li>
        <strong>Windows</strong> — the data path lives in a <strong>SYSTEM service</strong> installed
        once (elevated); every <code>plug</code> run is a non-elevated launcher that starts it on
        demand and delegates to it, so the command itself never needs admin.
      </li>
    </ul>

    <h3>Several clusters at once</h3>
    <p>
      Two different clusters can run side by side — <code>plug -p a</code> and
      <code>plug -p b</code> — with the same service names resolving to the right backend each time.
      How plug keeps them apart depends on the OS:
    </p>
    <ul>
      <li><strong>Linux</strong> gives every launch a private resolver in its own mount namespace, so two launches never share DNS — isolation for free.</li>
      <li><strong>Windows</strong> repoints the system resolver machine-wide, so the SYSTEM service holds one tunnel per cluster and disambiguates each flow <strong>at <code>connect()</code></strong>: the source port maps to the owning process, whose ancestry is walked back to the <code>plug -p x</code> that launched it — that is its cluster (PID-at-connect).</li>
      <li><strong>macOS</strong> — the global daemon holds one tunnel per cluster and attributes each flow by PID at connect, like Windows (proven simultaneously in CI).</li>
    </ul>
    <p>
      One honest limit: if a process <strong>fully detaches</strong> from the <code>plug</code> that
      launched it — a rare case, where it re-parents to the system and the ancestry link is lost —
      plug can no longer tell which cluster it belongs to, so it <strong>declines</strong> that
      connection rather than risk routing it to the wrong cluster.
    </p>

    <h3>The reverse direction</h3>
    <p>
      The transport carries the other way too: <code>plug -s
      &lt;name&gt;:&lt;cluster-port&gt;:&lt;local-port&gt; &lt;cmd&gt;</code> opens a
      <strong>dedicated SSH connection for the session</strong> and asks the agent's
      <code>sshd</code> for a standard <strong>remote forward</strong> — the agent listens on the
      cluster port, and every connection made by a cluster workload rides that session's connection
      back to the local port (dedicated so the port's lifetime is exactly the session's, even where
      the forward datapath lives in a shared daemon). The name is declared on the agent (a network alias in the stack file, a
      Service on Kubernetes), so cluster DNS does the routing; the listener lives and dies with the
      session, and plug verifies the whole loop at startup through the cluster's own DNS. See
      <a routerLink="/swarm">Agent &amp; Swarm</a> and
      <a routerLink="/kubernetes">Agent &amp; Kubernetes</a>.
    </p>

    <h3>Built with open source</h3>
    <p>plug stands on the shoulders of these projects — thank you:</p>
    <table>
      <thead>
        <tr><th>Dependency</th><th>Role</th><th>License</th></tr>
      </thead>
      <tbody>
        <tr><td><a href="https://www.openssh.com/" target="_blank" rel="noopener">OpenSSH</a></td><td>The transport: client (<code>golang.org/x/crypto/ssh</code>, in-process) and <code>sshd</code> in the agent doing the <code>direct-tcpip</code> dials</td><td>BSD</td></tr>
        <tr><td><a href="https://github.com/WireGuard/wireguard-go" target="_blank" rel="noopener">wireguard-go</a> + <a href="https://gvisor.dev/" target="_blank" rel="noopener">gVisor</a></td><td>The userspace TUN device and network stack that answer DNS and terminate flows in-process</td><td>MIT · Apache-2.0</td></tr>
        <tr><td><a href="https://go.dev/" target="_blank" rel="noopener">Go</a></td><td>The CLI — one static binary per platform, no runtime dependencies</td><td>BSD</td></tr>
        <tr><td><a href="https://www.alpinelinux.org/" target="_blank" rel="noopener">Alpine Linux</a></td><td>Base of the agent image — just <code>sshd</code> + the served binaries</td><td>MIT</td></tr>
      </tbody>
    </table>

    <div class="callout">
      <strong>Why not mirrord / Telepresence?</strong> Both are excellent — for Kubernetes. plug
      started because nothing equivalent existed for <strong>Docker Swarm</strong>, and now runs on
      both. Its agent is simple enough (a stock <code>sshd</code>) to embed later into another host —
      like an API gateway (see <a routerLink="/roadmap">Roadmap</a>) — and capturing at the IP layer
      buys full transparency across any runtime, Go and gRPC included, for the price of one privilege
      grant at install.
    </div>
  `,
})
export class HowItWorksComponent {}
