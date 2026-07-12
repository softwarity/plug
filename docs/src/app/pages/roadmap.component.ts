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
        color: #3fb950;
      }
      .status-icon.soon {
        color: var(--accent-yellow);
      }
    `,
  ],
  template: `
    <h2>Roadmap</h2>

    <div class="callout">
      <strong>How we got here.</strong> plug went through a few data paths — sshuttle, then userspace
      proxies with an injected <code>connect()</code>/DNS hook — before settling on today's
      <strong>userspace TUN</strong>. Answering DNS in-stack and capturing at the IP layer is what
      finally made it work under a corporate VPN <em>and</em> cover every runtime (Go and gRPC
      included) with no per-service config, while keeping several clusters isolated. See
      <a routerLink="/how-it-works">How it works</a>.
    </div>

    <h3>Kubernetes transport</h3>
    <p>
      The agent already runs on Kubernetes (a <a routerLink="/agent">manifest</a> with a NodePort, or
      <code>kubectl port-forward</code> for an RBAC-gated tunnel with no exposed port). Planned next:
      a <code>kubectl exec</code> transport (tunnel through <code>kubectl exec</code> to a plain pod:
      zero exposed port, access governed by each developer's kubeconfig RBAC — which also softens the
      <a routerLink="/security">no-auth trade-off</a>).
    </p>

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

    <div class="callout">
      <strong>No Homebrew tap, by design.</strong> plug is distributed <em>from the cluster</em> —
      the agent image is the single source of the CLI (<code>ssh get&#64;host install | sh</code>).
      A separate package channel (brew, apt…) would be a second source to keep in sync, so it is a
      deliberate non-goal.
    </div>

    <h3>Status</h3>
    <table>
      <thead>
        <tr><th>Item</th><th>State</th></tr>
      </thead>
      <tbody>
        <tr><td>Userspace-TUN data path (covers every runtime incl. Go &amp; gRPC)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Split-horizon routing (single-label → cluster, FQDN/localhost → direct)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Self-healing transport (SSH keepalive + transparent reconnect) + host-key pinning</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Install from cluster + launcher (per-cluster versions) + one-privilege install (setcap / setuid / service)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Profiles: wizard, <code>ls</code> / <code>rm</code> / <code>rn</code> / <code>test</code></td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>macOS DNS at the IP layer (works under a corporate VPN) + persistent per-cluster daemon</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Kubernetes manifest (NodePort / <code>kubectl port-forward</code>)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Windows no-admin SYSTEM service + multicluster (PID-at-connect)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Multicluster on Linux (per-launch mount namespaces)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
        <tr><td>Multicluster on macOS (same PID-at-connect design)</td><td><mat-icon class="status-icon soon">schedule</mat-icon> planned</td></tr>
        <tr><td><code>kubectl exec</code> transport (no exposed port)</td><td><mat-icon class="status-icon soon">schedule</mat-icon> planned</td></tr>
        <tr><td>Gateway hosting the tunnel + install surface</td><td><mat-icon class="status-icon soon">schedule</mat-icon> planned</td></tr>
        <tr><td>IPv6 fake-pool + v6-literal tunnelling (overlays are IPv4 today)</td><td><mat-icon class="status-icon soon">schedule</mat-icon> planned</td></tr>
        <tr><td>Native protocol e2e on every OS (8 protocols × 4 languages, by name over a mesh)</td><td><mat-icon class="status-icon ok">check_circle</mat-icon> shipped</td></tr>
      </tbody>
    </table>
  `,
})
export class RoadmapComponent {}
