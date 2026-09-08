import { Component } from '@angular/core';
import { MatIconModule } from '@angular/material/icon';
import { RouterLink } from '@angular/router';

@Component({
  selector: 'app-comparison',
  imports: [MatIconModule, RouterLink],
  preserveWhitespaces: true,
  styles: [
    `
      .cmp {
        overflow-x: auto;
        margin: 4px 0 18px;
        border: 1px solid var(--border-color);
        border-radius: 10px;
      }
      .cmp table {
        border-collapse: collapse;
        width: 100%;
        min-width: 660px;
        font-size: 0.85rem;
        margin: 0;
      }
      .cmp th,
      .cmp td {
        padding: 9px 13px;
        border-bottom: 1px solid var(--border-color);
        text-align: left;
        vertical-align: top;
      }
      .cmp tbody tr:last-child td {
        border-bottom: none;
      }
      .cmp thead th {
        font-size: 0.7rem;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--text-muted);
        background: var(--bg-secondary);
        font-weight: 600;
      }
      .cmp tbody td:first-child {
        color: var(--text-secondary);
        white-space: nowrap;
      }
      .cmp th:nth-child(2),
      .cmp td:nth-child(2) {
        background: rgba(163, 113, 247, 0.08);
        color: var(--text-primary);
      }
      .cmp-note {
        color: var(--text-secondary);
        font-size: 0.88rem;
        line-height: 1.5;
        background: var(--bg-secondary);
        border: 1px solid var(--border-color);
        border-radius: 8px;
        padding: 12px 15px;
        margin: 0 0 24px;
      }
      .cmp-note strong {
        color: var(--text-primary);
      }
      .cta {
        margin: 4px 0 8px;
      }
      .cta a {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        padding: 10px 18px;
        border-radius: 8px;
        background: var(--accent-purple);
        color: #0d1117;
        font-weight: 600;
        text-decoration: none;
      }
      .cta a:hover {
        text-decoration: none;
        filter: brightness(1.08);
      }
    `,
  ],
  template: `
    <h2>How plug compares</h2>

    <p>
      <a href="https://metalbear.com/mirrord/" target="_blank" rel="noopener">mirrord</a> and
      <a href="https://telepresence.io/" target="_blank" rel="noopener">Telepresence</a> are the
      well-known <strong>Kubernetes-native</strong> tools for running local code against a remote
      cluster. plug's angle is different: the same behaviour on Docker, Compose, Swarm
      <em>and</em> Kubernetes, and nothing to hold on a developer's machine.
    </p>

    <div class="cmp">
      <table>
        <thead>
          <tr><th></th><th scope="col">plug</th><th scope="col">mirrord</th><th scope="col">Telepresence</th></tr>
        </thead>
        <tbody>
          <tr><td>Cluster targets</td><td>Docker · Compose · Swarm · Kubernetes</td><td>Kubernetes</td><td>Kubernetes / OpenShift</td></tr>
          <tr><td>Your machine</td><td>Linux · macOS · Windows (amd64 + arm64)</td><td>Linux · macOS · Windows</td><td>Linux · macOS · Windows</td></tr>
          <tr><td>What a developer needs</td><td><strong>plug, and a route to the agent</strong></td><td>a kubeconfig with rights on the cluster</td><td>a kubeconfig with rights on the cluster</td></tr>
          <tr><td>Setup, your machine</td><td>one ssh command, served by the cluster</td><td>brew / curl / choco</td><td>package or installer</td></tr>
          <tr><td>Setup, cluster side</td><td>one agent container</td><td>none</td><td>traffic-manager</td></tr>
          <tr><td>Reach cluster services by name</td><td>✓</td><td>✓</td><td>✓</td></tr>
          <tr><td>Be reachable by a cluster name</td><td>✓</td><td>✓ steal / mirror</td><td>✓ intercept</td></tr>
          <tr><td>Any runtime, no code change</td><td>✓ (IP layer)</td><td>✓</td><td>✓</td></tr>
          <tr><td>Run a container as a member</td><td>✓ <code>--dockerrun</code></td><td>✓ <code>mirrord container</code></td><td>✓ <code>--docker-run</code></td></tr>
          <tr><td>Several devs on one shared service</td><td>one name, one session</td><td>header / queue split</td><td>header / path</td></tr>
          <tr><td>Auth</td><td>none - trusted dev cluster</td><td>kubeconfig RBAC</td><td>kubeconfig RBAC</td></tr>
          <tr><td>IDE extensions</td><td>none, CLI only</td><td>VS Code · JetBrains</td><td>JetBrains</td></tr>
          <tr><td>Mechanism</td><td>userspace TUN over SSH</td><td>syscall layer in your process</td><td>in-cluster traffic-manager</td></tr>
          <tr><td>Behind it</td><td>softwarity, the team behind <a routerLink="/meerkat">Meerkat</a></td><td>MetalBear</td><td>Ambassador Labs, now a CNCF project</td></tr>
          <tr><td>License</td><td>FSL-1.1-Apache-2.0</td><td>MIT</td><td>Apache-2.0</td></tr>
        </tbody>
      </table>
    </div>

    <p class="cmp-note">
      <strong>Why plug:</strong> it behaves the same whichever backend provisions the name, and a
      developer needs nothing but plug - no Kubernetes tooling, no account in the cluster. The setup
      lives once in the cluster instead of on every desk, which is also why it works where a
      kubeconfig does not exist at all: Docker, Compose, Swarm.
    </p>

    <p class="cmp-note">
      <strong>Where they are ahead:</strong> both authenticate through your kubeconfig and its RBAC,
      where plug on its own trusts whoever reaches the agent (see the
      <a routerLink="/security">security model</a>, and <a routerLink="/meerkat">Meerkat</a> for
      named identities). Both split one shared service between several developers by routing on a
      header or a path. And both are older, with IDE extensions and a larger community.
    </p>

    <p class="cta">
      <a routerLink="/getting-started">Set it up <mat-icon aria-hidden="true" style="font-size:18px;width:18px;height:18px">arrow_forward</mat-icon></a>
    </p>
  `,
})
export class ComparisonComponent {}
