import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { MatIconModule } from '@angular/material/icon';

@Component({
  selector: 'app-roadmap',
  imports: [RouterLink, MatIconModule],
  preserveWhitespaces: true,
  styles: [
    `
      .status-icon {
        font-size: 18px;
        width: 18px;
        height: 18px;
        vertical-align: middle;
        margin-right: 3px;
      }
      .status-icon.ok {
        color: var(--accent-green);
      }
      .status-icon.soon {
        color: var(--accent-yellow);
      }
      /* Neither promised nor finished. Without it, a line that is half done has
         to lie in one direction or the other, and both readings mislead. */
      .status-icon.partial {
        color: var(--accent-blue);
      }
    `,
  ],
  template: `
    <h2>Roadmap</h2>

    <div class="callout">
      <strong>How we got here.</strong> plug went through a few data paths - sshuttle, then userspace
      proxies with an injected <code>connect()</code>/DNS hook - before settling on today's
      <strong>userspace TUN</strong>. Answering DNS in-stack and capturing at the IP layer is what
      finally made it work under a corporate VPN <em>and</em> cover every runtime (Go and gRPC
      included) with no per-service config, while keeping several clusters isolated. See
      <a routerLink="/how-it-works">How it works</a>.
    </div>

    <h3>Kubernetes transport</h3>
    <p>
      The agent already runs on Kubernetes (a <a routerLink="/kubernetes">manifest</a> with a NodePort, or
      <code>kubectl port-forward</code> for an RBAC-gated tunnel with no exposed port). Nothing
      Kubernetes-only is planned beyond that, and it is a decision rather than an omission. A
      <code>kubectl exec</code> transport was considered twice and dropped twice, for two reasons that
      still hold: <code>port-forward</code> already gives the zero-exposed port gated by API-server
      RBAC that it would have bought, and plug behaves the same way whichever backend provisions the
      name - a transport existing in one family alone would trade that for a second code path to carry
      forever. Where the <a routerLink="/security">no-auth trade-off</a> has to go away, the answer is
      the <a routerLink="/meerkat">gateway</a> hosting the tunnel, which covers every family instead
      of one.
    </p>

    <h3>UDP by name</h3>
    <p>
      The tunnel carries <strong>TCP only</strong> - SSH's <code>direct-tcpip</code> is stream-only,
      so UDP to a cluster service is not forwarded today (DNS is the exception, answered in-stack;
      see <a routerLink="/how-it-works">How it works</a>). Planned: a <strong>datagram relay</strong>.
      The agent gains a small <code>udp-relay</code> helper - invoked over SSH exactly like the
      <code>-s</code> provisioning - while plug frames datagrams over a channel and pumps them both
      ways, reusing the same by-name lookup and per-cluster attribution as TCP. The trade-off is
      honest: datagrams then ride a reliable, ordered stream (with head-of-line blocking), which fits
      DNS-over-UDP, StatsD, syslog and request/response UDP, but not real-time media. QUIC and HTTP/3
      (UDP-based) would stop being silently dropped - though most clients already fall back to TCP.
      Landing first, on its own: cluster UDP that is dropped today fails <em>silently</em> (it looks
      like a hang) - plug will <strong>log</strong> it instead, so it fails loud.
    </p>

    <h3>API-gateway integration</h3>
    <p>
      The end game: no dedicated agent at all. The API gateway already deployed in the
      cluster hosts the tunnel endpoint and turns it on and off dynamically - dev tooling that
      piggybacks on infrastructure you already trust, with the gateway's own authentication in
      front. The install and versioning contract already exists (see below); the gateway will
      simply expose the same surface, so the CLI will not need to relearn anything. That gateway is
      <a href="https://softwarity.github.io/meerkat/" target="_blank" rel="noopener">Meerkat</a>,
      a companion project.
    </p>
    <p>
      Part of it is here. Meerkat's Enterprise edition already integrates plug and gives sessions an
      <strong>identity</strong>: a developer deposits their own public key through the gateway, it is
      tied to their name, and who is plugged into what stops being anonymous. plug ships the CLI
      flavour that goes with it, published and signed beside the standalone one. What is still ahead
      is the end of the dedicated agent - the gateway hosting the tunnel itself. See
      <a routerLink="/meerkat">Meerkat</a> for the model.
    </p>

    <div class="callout">
      <strong>Shipped already:</strong> installing the CLI from the cluster and per-cluster version
      matching were on this roadmap - they now work over the agent's SSH port. The passwordless
      <code>get</code> user serves an installer (<code>ssh get&#64;host install | sh</code>), and the
      <a routerLink="/profiles">launcher</a> runs each cluster's exact version. No GitHub access
      required, no extra port, no HTTP server.
    </div>

    <div class="callout">
      <strong>No Homebrew tap, by design.</strong> plug is distributed <em>from the cluster</em> -
      the agent image is the single source of the CLI (<code>ssh get&#64;host install | sh</code>).
      A separate package channel (brew, apt…) would be a second source to keep in sync, so it is a
      deliberate non-goal.
    </div>

    <h3>Talking to a running session</h3>
    <p>
      A session prints what it does and otherwise stays out of the way, which leaves two questions
      unanswered while it runs: <em>is my name actually reachable right now</em>, and <em>what is
      that other session of mine still holding</em>. Planned: a separate command -
      <code>plug status</code> - listing the sessions alive on this machine, the names they serve
      and the state of each path, plus verbs to act on one (stop it, re-provision its name) from
      any terminal.
    </p>
    <p>
      <strong>Deliberately not keystrokes in the running session.</strong> Your command owns
      <code>stdin</code>: Vite reads <code>r</code>, <code>u</code> and <code>q</code>, nodemon
      reads <code>rs</code>, a REPL reads everything. Any key plug claimed would be a key stolen
      from the program it launched, and there is no key that is free across every runtime someone
      might run under it. An out-of-band command has none of that problem, works from a terminal
      you still have, and - unlike a keystroke - reaches the session whose terminal you closed.
    </p>

    <h3>Status</h3>
    <table>
      <thead>
        <tr><th scope="col">Item</th><th scope="col">State</th></tr>
      </thead>
      <tbody>
        <tr><td>Userspace-TUN data path (covers every runtime incl. Go &amp; gRPC)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Split-horizon routing (single-label → cluster, FQDN/localhost → direct)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Self-healing transport (SSH keepalive + transparent reconnect) + host-key pinning</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Install from cluster + launcher (per-cluster versions) + one-privilege install (setcap / setuid / service)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Profiles: wizard, <code>ls</code> / <code>rm</code> / <code>rn</code> / <code>test</code></td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>macOS DNS at the IP layer (works under a corporate VPN) + persistent per-cluster daemon</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Forwarding follows the machine's resolvers when a VPN comes up or drops, or the network changes (<code>plug doctor</code> reports where lookups go)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Kubernetes manifest (NodePort / <code>kubectl port-forward</code>)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Windows no-admin SYSTEM service + multicluster (PID-at-connect)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Multicluster on Linux (per-launch mount namespaces)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Multicluster on macOS (same PID-at-connect design)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Reverse direction: serve a local port to the cluster under a cluster name (<code>-s</code>), name provisioned dynamically (docker-sock / k8s-RBAC)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Takeover (default): a deployed service owning a <code>-s</code> name is parked for the session and restored on exit</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td><code>plug status</code> and verbs to act on a running session, out of band (never a keystroke: your command owns stdin)</td><td><mat-icon class="status-icon soon">schedule</mat-icon> planned</td></tr>
        <tr><td>Gateway hosting the tunnel + install surface (<a routerLink="/meerkat">Meerkat</a>): the hosted CLI flavour and the named-identity model ship today; the gateway replacing the agent outright does not</td><td><mat-icon class="status-icon partial">timelapse</mat-icon> in progress</td></tr>
        <tr><td>IPv6 fake-pool + v6-literal tunnelling (overlays are IPv4 today)</td><td><mat-icon class="status-icon soon">schedule</mat-icon> planned</td></tr>
        <tr><td>UDP by name - framed datagram relay over the tunnel (TCP-only today)</td><td><mat-icon class="status-icon soon">schedule</mat-icon> planned</td></tr>
        <tr><td>Native protocol e2e on every OS (8 protocols × 4 languages, by name over a mesh)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
      </tbody>
    </table>
  `,
})
export class RoadmapComponent {}
