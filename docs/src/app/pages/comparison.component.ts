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
      /* Where plug is ahead. Declared AFTER the column rule on purpose: both have
         the same specificity, so the later one wins and the green replaces the
         column's purple rather than fighting it. Only cells that are ahead on a
         fact stated in the same row - nothing here is highlighted for emphasis. */
      .cmp td.win {
        background: rgba(63, 185, 80, 0.14);
        color: var(--text-primary);
        font-weight: 600;
      }
      .cmp-legend {
        color: var(--text-muted);
        font-size: 0.78rem;
        margin: -10px 0 18px;
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
          <tr><td>Cluster targets</td><td class="win">Docker · Compose · Swarm · Kubernetes</td><td>Kubernetes</td><td>Kubernetes / OpenShift</td></tr>
          <tr><td>Your machine</td><td class="win">Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64, arm64</td><td>Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64</td><td class="win">Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64, arm64</td></tr>
          <tr><td>What a developer needs</td><td class="win">the cluster's address - plug installs itself from it</td><td>a kubeconfig with rights on the cluster</td><td>a kubeconfig with rights on the cluster</td></tr>
          <tr><td>What you point it at</td><td class="win">a name - which need not exist in the cluster yet, and can still take over one that does</td><td>an existing workload, named with <code>--target</code></td><td>an existing service, to intercept</td></tr>
          <tr><td>Setup, your machine</td><td class="win">one ssh command, served by the cluster - always the version the agent runs</td><td>brew / curl / choco</td><td>package or installer</td></tr>
          <tr><td>Staying up to date</td><td class="win">the launcher follows the version its cluster's agent serves, per cluster: a session on an updated cluster downloads that core once and runs it; <code>plug update</code> moves the agent, <code>update=notify|auto</code> says or does it for you</td><td>reinstall the CLI (brew / curl); the operator is upgraded separately</td><td>reinstall the CLI (brew / package); the traffic-manager is upgraded separately, by Helm</td></tr>
          <tr><td>Several clusters on several versions</td><td class="win">one launcher, one cached core per version - each profile runs what its agent serves, nothing to pick</td><td>one CLI version at a time</td><td>one CLI version at a time</td></tr>
          <tr><td>Setup, cluster side</td><td>one agent container</td><td class="win">none</td><td>traffic-manager</td></tr>
          <tr><td>Reach cluster services by name</td><td>✓</td><td>✓</td><td>✓</td></tr>
          <tr><td>Be reachable by a cluster name</td><td class="win">✓ <code>-s name:8080:3000</code> - the name is provisioned for the session</td><td>by stealing or mirroring an existing pod's traffic</td><td>by intercepting an existing service</td></tr>
          <tr><td>Any runtime, no code change</td><td>✓ (IP layer)</td><td>✓</td><td>✓</td></tr>
          <tr><td>Run a container as a member</td><td>✓ <code>--dockerrun</code></td><td>✓ <code>mirrord container</code></td><td>✓ <code>--docker-run</code></td></tr>
          <tr><td>Inherit the workload's environment</td><td class="win">✓ by default on a takeover, secrets as the pod has them; yours win, and <code>--no-env</code> opts out whole OR key by key (<code>--no-env DB_URL,API_KEY</code>)</td><td>✓ by default, from the target pod</td><td>✓ on intercept, or <code>--env-file</code></td></tr>
          <tr><td>Borrow ANOTHER workload's environment</td><td class="win">✓ <code>--env-of &lt;name&gt;</code> - a pure client (<code>-c</code>) run with a service's own credentials, or a <code>-s</code> served with the variables of the service it talks to, without parking it</td><td>only the <code>--target</code> you intercept</td><td>only the service you intercept</td></tr>
          <tr><td>Its mounted secret/config FILES (a CA, a keystore)</td><td class="win">✓ <strong>no FUSE</strong>: the files are read through the agent's existing exec/API and materialised for the session (or mounted at their exact path under <code>--dockerrun</code>); the variables that name them are repointed. Kubernetes and Compose secrets, Swarm configs</td><td>fs mirroring, live - needs its syscall layer in-process</td><td>volume mounts, live - via <strong>sshfs / FUSE</strong> (macFUSE, WinFSP)</td></tr>
          <tr><td>Several devs on one shared service</td><td>one name, one session</td><td class="win">header / queue split</td><td class="win">header / path</td></tr>
          <tr><td>Auth</td><td>none on its own - trusted dev cluster; named per-developer identities with <a routerLink="/meerkat">Meerkat</a></td><td class="win">kubeconfig RBAC</td><td class="win">kubeconfig RBAC</td></tr>
          <tr><td>IDE extensions</td><td>none, CLI only - there is no target to pick, so nothing for a panel to show</td><td class="win">VS Code · JetBrains</td><td>JetBrains</td></tr>
          <tr><td>For an AI coding agent</td><td class="win">✓ <a routerLink="/mcp"><code>plug mcp</code></a>, an MCP server over stdio: the clusters this machine knows, <code>doctor</code> check by check, a workload's environment, whether a name exists, as tools with structured answers</td><td>the agent drives the CLI like a developer would; no MCP documented</td><td>nothing documented</td></tr>
          <tr><td>Mechanism</td><td>userspace TUN over SSH</td><td>syscall layer in your process</td><td>in-cluster traffic-manager</td></tr>
          <tr><td>Behind it</td><td>softwarity, the team behind <a routerLink="/meerkat">Meerkat</a></td><td>MetalBear</td><td>Ambassador Labs, now a CNCF project</td></tr>
          <tr><td>License</td><td>FSL-1.1-Apache-2.0</td><td>MIT</td><td>Apache-2.0</td></tr>
        </tbody>
      </table>
    </div>

    <p class="cmp-legend">Green marks whichever tool is ahead on the fact stated in that row - including when it is not plug, and both of them when two are ahead of the third. Rows where all three are level are left plain.</p>

    <p class="cmp-note">
      <strong>Why plug:</strong> it behaves the same whichever backend provisions the name, and a
      developer needs nothing but the cluster's address - no Kubernetes tooling, no account in the
      cluster. The setup lives once in the cluster instead of on every desk, which is also why it
      works where a kubeconfig does not exist at all: Docker, Compose, Swarm.
    </p>

    <p class="cmp-note">
      <strong>And there is nothing to point at.</strong> Both of the others work by substitution:
      you name an existing workload and they stand in its place, which is why they need a target and
      a dialog to pick it. plug ADDS a member to the cluster, so you declare a name and that is the
      whole of it - a name that <em>does not have to exist yet</em>. The service you are writing
      right now, which is deployed nowhere, is reachable from the rest of the stack by its future
      name. And when a deployed workload does own that name, plug parks it for the session and puts
      it back on the way out.
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
