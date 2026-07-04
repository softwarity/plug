import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-roadmap',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Roadmap</h2>

    <div class="callout">
      <strong>How we got here.</strong> plug started on sshuttle, then a native Go TUN + a
      split-horizon DNS resolver. On a corporate VPN, transparent DNS fights the VPN's own resolver;
      and full multi-cluster (same service names, at once) can't work with <em>any</em> global
      interception. So plug settled on a <strong>rootless SOCKS5 proxy + per-session port-forwards</strong>:
      no global state, native multi-cluster, and the SSH transport we'd already built. Full
      syscall-level transparency for non-cooperating drivers is mirrord's domain (library injection),
      a deliberate non-goal here.
    </div>

    <h3>Kubernetes transport</h3>
    <p>
      The SOCKS transport is agnostic to the orchestrator — it only needs an SSH agent reachable in
      the cluster. Planned: a Kubernetes deployment for the agent, and a
      <code>kubectl exec</code> transport (tunnel through <code>kubectl exec</code> to a plain pod:
      zero exposed port, access governed by each developer's kubeconfig RBAC — which also softens
      the <a routerLink="/security">no-auth trade-off</a>).
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
      deliberate non-goal. GitHub Releases remain as a fallback download.
    </div>

    <h3>Status</h3>
    <table>
      <thead>
        <tr><th>Item</th><th>State</th></tr>
      </thead>
      <tbody>
        <tr><td>Rootless SOCKS5 proxy + per-session port-forwards</td><td>✅ shipped</td></tr>
        <tr><td>Multi-cluster in parallel (compare environments)</td><td>✅ shipped</td></tr>
        <tr><td>Install from cluster + launcher (per-cluster versions)</td><td>✅ shipped</td></tr>
        <tr><td>Profiles: wizard, <code>ls</code> / <code>rm</code> / <code>rn</code> / <code>test</code></td><td>✅ shipped</td></tr>
        <tr><td>Kubernetes manifest (NodePort / <code>kubectl port-forward</code>)</td><td>✅ shipped</td></tr>
        <tr><td><code>kubectl exec</code> transport (no exposed port)</td><td>🔜 planned</td></tr>
        <tr><td>Gateway hosting the tunnel + install surface</td><td>🔜 planned</td></tr>
      </tbody>
    </table>
  `,
})
export class RoadmapComponent {}
