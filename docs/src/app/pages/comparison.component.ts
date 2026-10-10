import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CtaComponent } from '../cta/cta.component';

@Component({
  selector: 'app-comparison',
  imports: [CtaComponent, RouterLink],
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
      .cmp tbody tr:last-child td,
      .cmp tbody tr:last-child th {
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
      /* The first column is the row's header (th scope="row") for assistive
         tech; visually it stays the plain label it was as a td, so the global
         th look (shaded, bold) is undone here. */
      .cmp tbody th {
        color: var(--text-secondary);
        font-weight: 400;
        background: transparent;
      }
      /* The criteria column is narrow and its labels wrap: the room goes to the
         three columns being compared, which carry the sentences. */
      .cmp th:first-child {
        width: 16%;
        min-width: 7.5rem;
      }
      .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        overflow: hidden;
        clip: rect(0 0 0 0);
        white-space: nowrap;
      }
      .cmp th:nth-child(2),
      .cmp td:nth-child(2) {
        background: var(--accent-purple-tint);
        color: var(--text-primary);
        border-left: 2px solid var(--accent-purple);
        border-right: 2px solid var(--accent-purple);
      }
      /* plug's column, framed top to bottom. With collapsed borders the wider
         line wins where two meet, so the frame stays whole across the 1px row
         separators; the last row keeps its bottom edge for the frame alone. */
      .cmp thead th:nth-child(2) {
        border-top: 2px solid var(--accent-purple);
        color: var(--accent-purple);
      }
      .cmp tbody tr:last-child td:nth-child(2) {
        border-bottom: 2px solid var(--accent-purple);
      }
      /* Where plug is ahead. Declared AFTER the column rule on purpose: both have
         the same specificity, so the later one wins and the green replaces the
         column's purple rather than fighting it. Only cells that are ahead on a
         fact stated in the same row - nothing here is highlighted for emphasis. */
      .cmp td.win {
        background: var(--accent-green-bg);
        color: var(--text-primary);
        font-weight: 600;
      }
      .cmp-legend {
        color: var(--text-muted);
        font-size: 0.78rem;
        margin: -10px 0 18px;
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
          <tr><th scope="col"><span class="sr-only">Criterion</span></th><th scope="col">plug</th><th scope="col">mirrord</th><th scope="col">Telepresence</th></tr>
        </thead>
        <tbody>
          <tr><th scope="row">Cluster targets</th><td class="win">Docker · Compose · Swarm · Kubernetes</td><td>Kubernetes</td><td>Kubernetes / OpenShift</td></tr>
          <tr><th scope="row">Your machine</th><td class="win">Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64, arm64</td><td>Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64</td><td class="win">Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64, arm64</td></tr>
          <tr><th scope="row">What a developer needs</th><td class="win">the cluster's address - plug installs itself from it</td><td>a kubeconfig with rights on the cluster</td><td>a kubeconfig with rights on the cluster</td></tr>
          <tr><th scope="row">What you point it at</th><td class="win">a name - which need not exist in the cluster yet, and can still take over one that does</td><td>an existing workload, named with <code>--target</code></td><td>an existing service, to intercept</td></tr>
          <tr><th scope="row">Setup, your machine</th><td class="win">one ssh command, served by the cluster - always the version the agent runs</td><td>brew / curl / choco</td><td>package or installer</td></tr>
          <tr><th scope="row">Staying up to date</th><td class="win">the launcher follows the version its cluster's agent serves, per cluster: a session on an updated cluster downloads that core once and runs it; <code>plug update</code> moves the agent, <code>update=notify|auto</code> says or does it for you</td><td>reinstall the CLI (brew / curl); the operator is upgraded separately</td><td>reinstall the CLI (brew / package); the traffic-manager is upgraded separately, by Helm</td></tr>
          <tr><th scope="row">Several clusters on several versions</th><td class="win">one launcher, one cached core per version - each profile runs what its agent serves, nothing to pick</td><td>one CLI version at a time</td><td>one CLI version at a time</td></tr>
          <tr><th scope="row">Setup, cluster side</th><td>one agent container</td><td class="win">none</td><td>traffic-manager</td></tr>
          <tr><th scope="row">Reach cluster services by name</th><td>✓</td><td>✓</td><td>✓</td></tr>
          <tr><th scope="row">Be reachable by a cluster name</th><td class="win">✓ <code>-s name:8080:3000</code> - the name is provisioned for the session</td><td>by stealing or mirroring an existing pod's traffic</td><td>by intercepting an existing service</td></tr>
          <tr><th scope="row">Any runtime, no code change</th><td>✓ (IP layer)</td><td>✓</td><td>✓</td></tr>
          <tr><th scope="row">Run a container as a member</th><td>✓ <code>--dockerrun</code></td><td>✓ <code>mirrord container</code></td><td>✓ <code>--docker-run</code></td></tr>
          <tr><th scope="row">Inherit the workload's environment</th><td class="win">✓ by default on a takeover, secrets as the pod has them; the <strong>cluster's value wins</strong> over an inherited one (behaves as in the cluster), and <code>--no-env</code> keeps yours instead - whole OR key by key (<code>--no-env DB_URL,API_KEY</code>)</td><td>✓ by default, from the target pod</td><td>✓ on intercept, or <code>--env-file</code></td></tr>
          <tr><th scope="row">Borrow ANOTHER workload's environment</th><td class="win">✓ <code>--env-of &lt;name&gt;</code> - a pure client (<code>-c</code>) run with a service's own credentials, or a <code>-s</code> served with the variables of the service it talks to, without parking it</td><td>only the <code>--target</code> you intercept</td><td>only the service you intercept</td></tr>
          <tr><th scope="row">Its mounted secret/config FILES (a CA, a keystore)</th><td class="win">✓ <strong>no FUSE</strong>: the files are read through the agent's existing exec/API and materialised for the session (or mounted at their exact path under <code>--dockerrun</code>); the variables that name them are repointed. Kubernetes and Compose secrets, Swarm configs</td><td>fs mirroring, live - needs its syscall layer in-process</td><td>volume mounts, live - via <strong>sshfs / FUSE</strong> (macFUSE, WinFSP)</td></tr>
          <tr><th scope="row">Its data VOLUMES / PVCs, live</th><td class="win">✓ <strong>by default, nothing to install</strong>: every volume of the workload a <code>-s</code> takes over (or <code>--env-of</code> names) is mounted on your machine read-write, and the variables naming it are repointed - the process finds its data and knows nothing (<code>--no-mount</code> to opt out, <code>--mount</code> for an exact path). Mounted with the SMB client the OS already has, on all three OSes; the agent runs a helper beside the workload, tied to the session like the name</td><td>✓ file calls hooked in your process and relayed into the pod one by one (the runtime has to be hookable)</td><td>✓ <code>--mount</code>, through sshfs - a FUSE runtime and sshfs to install first</td></tr>
          <tr><th scope="row">Several devs on one shared service</th><td>one name, one session</td><td class="win">header / queue split</td><td class="win">header / path</td></tr>
          <tr><th scope="row">Auth</th><td>none on its own - trusted dev cluster; named per-developer identities with <a routerLink="/meerkat">Meerkat</a></td><td class="win">kubeconfig RBAC</td><td class="win">kubeconfig RBAC</td></tr>
          <tr><th scope="row">IDE extensions</th><td>none, CLI only - there is no target to pick, so nothing for a panel to show</td><td class="win">VS Code · JetBrains</td><td>JetBrains</td></tr>
          <tr><th scope="row">For an AI coding agent</th><td class="win">✓ <a routerLink="/mcp"><code>plug mcp</code></a>, an MCP server over stdio: the clusters this machine knows, <code>doctor</code> check by check, a workload's environment, whether a name exists, as tools with structured answers</td><td>the agent drives the CLI like a developer would; no MCP documented</td><td>nothing documented</td></tr>
          <tr><th scope="row">Mechanism</th><td>userspace TUN over SSH</td><td>syscall layer in your process</td><td>in-cluster traffic-manager</td></tr>
          <tr><th scope="row">Behind it</th><td>softwarity, the team behind <a routerLink="/meerkat">Meerkat</a></td><td>MetalBear</td><td>Ambassador Labs, now a CNCF project</td></tr>
          <tr><th scope="row">License</th><td>FSL-1.1-Apache-2.0</td><td>MIT</td><td>Apache-2.0</td></tr>
        </tbody>
      </table>
    </div>

    <p class="cmp-legend">Green marks whichever tool is ahead on the fact stated in that row - including when it is not plug, and both of them when two are ahead of the third. Rows where all three are level are left plain.</p>

    <p class="note">
      <strong>Why plug:</strong> it behaves the same whichever backend provisions the name, and a
      developer needs nothing but the cluster's address - no Kubernetes tooling, no account in the
      cluster. The setup lives once in the cluster instead of on every desk, which is also why it
      works where a kubeconfig does not exist at all: Docker, Compose, Swarm.
    </p>

    <p class="note">
      <strong>And there is nothing to point at.</strong> Both of the others work by substitution:
      you name an existing workload and they stand in its place, which is why they need a target and
      a dialog to pick it. plug ADDS a member to the cluster, so you declare a name and that is the
      whole of it - a name that <em>does not have to exist yet</em>. The service you are writing
      right now, which is deployed nowhere, is reachable from the rest of the stack by its future
      name. And when a deployed workload does own that name, plug parks it for the session and puts
      it back on the way out.
    </p>

    <p class="note">
      <strong>Where they are ahead:</strong> both authenticate through your kubeconfig and its RBAC,
      where plug on its own trusts whoever reaches the agent (see the
      <a routerLink="/security">security model</a>, and <a routerLink="/meerkat">Meerkat</a> for
      named identities). Both split one shared service between several developers by routing on a
      header or a path. And both are older, with IDE extensions and a larger community.
    </p>

    <app-cta link="/getting-started">Set it up</app-cta>
  `,
})
export class ComparisonComponent {}
