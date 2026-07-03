import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-roadmap',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Roadmap</h2>

    <h3>Native Go tunnel — zero dependency (next)</h3>
    <p>
      Today plug shells out to <a href="https://github.com/sshuttle/sshuttle" target="_blank"
      rel="noopener">sshuttle</a>, so a dev has to install it (and a Python) first. The goal: a
      <strong>single self-contained binary</strong> that does the tunnel itself — nothing else to
      install.
    </p>
    <p>The design, and why it also <em>simplifies</em> the agent:</p>
    <ul>
      <li><strong>Datapath in Go.</strong> A userspace TUN interface + a netstack (the tailscale / wireguard-go approach) captures traffic to the cluster subnets and terminates the flows in-process.</li>
      <li><strong>Transport = plain SSH.</strong> Each captured flow becomes an SSH <code>direct-tcpip</code> channel to <code>service:port</code> — a stock <code>sshd</code> feature. DNS is forwarded the same way, over TCP, to the cluster resolver (<code>127.0.0.11:53</code>) which <code>sshd</code> can reach from inside the agent.</li>
      <li><strong>The agent loses Python.</strong> No more sshuttle server half: the image becomes just <code>sshd</code> (with TCP forwarding for the <code>plug</code> user) plus the served binaries. Smaller, simpler, one less moving part.</li>
    </ul>
    <p>
      The hard, platform-specific part is the client datapath (utun/pf on macOS, tun/nftables on
      Linux, IPv6) — it needs the real cluster to validate, so it lands as its own focused pass.
      Root is still required locally (as with sshuttle), for the TUN device and routes.
    </p>

    <h3>Kubernetes transport</h3>
    <p>
      The tunnel mechanics work on Kubernetes today — what's missing is the turnkey part. Two gaps,
      two answers:
    </p>
    <ul>
      <li>
        <strong>Service CIDR discovery.</strong> A pod only sees its own IP; the ClusterIP range is
        virtual (iptables/IPVS), so <a routerLink="/how-it-works">interface-based discovery</a>
        can't find it. Planned: ask the apiserver. Meanwhile, pin it in the profile:
      </li>
    </ul>
    <app-code lang="text"># ~/.plug/kube-dev.conf — works today
host = a-node.example.com
port = 2222
subnets = 10.96.0.0/12,10.244.0.0/16   # service CIDR + pod CIDR</app-code>
    <ul>
      <li>
        <strong><code>kubectl exec</code> transport.</strong> Instead of a published port, tunnel
        through <code>kubectl exec</code> towards a plain pod: zero exposed surface, and access is
        governed by each developer's kubeconfig RBAC — a natural fit that also softens the
        <a routerLink="/security">no-auth trade-off</a>.
      </li>
    </ul>

    <h3>API-gateway integration</h3>
    <p>
      The end game: no dedicated agent at all. The (Java) API gateway already deployed in the
      cluster hosts the tunnel endpoint and turns it on and off dynamically — dev tooling that
      piggybacks on infrastructure you already trust, with the gateway's own authentication in
      front. The install and versioning contract already exists (see below); the gateway will
      simply expose the same surface, so the CLI will not need to relearn anything.
    </p>

    <div class="callout">
      <strong>Shipped already:</strong> installing the CLI from the cluster and per-cluster version
      matching were on this roadmap — they now work over the agent's SSH port. The passwordless
      <code>get</code> user serves an installer (<code>ssh get&#64;host install | sh</code>), and the
      <a routerLink="/profiles">launcher</a> runs each cluster's exact version. No GitHub access
      required, no extra port, no HTTP server.
    </div>

    <h3>Homebrew tap</h3>
    <p>One-liner install and upgrades on macOS/Linux:</p>
    <app-code lang="bash">brew tap softwarity/tap
brew install plug</app-code>
    <p>
      A release-workflow step will regenerate the formula (URL + checksums) on every version.
    </p>

    <h3>Status</h3>
    <table>
      <thead>
        <tr><th>Item</th><th>State</th></tr>
      </thead>
      <tbody>
        <tr><td>Docker Swarm, auto-discovery, profiles &amp; wizard</td><td>✅ shipped</td></tr>
        <tr><td>Install from cluster + launcher (per-cluster versions)</td><td>✅ shipped</td></tr>
        <tr><td>Kubernetes with manual <code>subnets =</code></td><td>✅ works today</td></tr>
        <tr><td>Native Go tunnel (drop sshuttle &amp; Python)</td><td>🔜 next</td></tr>
        <tr><td>Kubernetes turnkey (CIDR discovery, <code>kubectl exec</code>)</td><td>🔜 planned</td></tr>
        <tr><td>Gateway hosting the tunnel + install surface</td><td>🔜 planned</td></tr>
        <tr><td>Homebrew tap</td><td>🔜 planned</td></tr>
      </tbody>
    </table>
  `,
})
export class RoadmapComponent {}
