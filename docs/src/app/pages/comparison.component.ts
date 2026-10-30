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
        /* Fixed: the widths below are kept, not stretched to the longest
           sentence, so plug's column, which says the most, gets no more room
           than the others. */
        table-layout: fixed;
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
         three columns being compared, which carry the sentences, in equal shares. */
      .cmp thead th:first-child {
        width: 16%;
      }
      .cmp thead th:not(:first-child) {
        width: 28%;
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
      <a href="https://telepresence.io/" target="_blank" rel="noopener">Telepresence</a> are two
      established tools for running local code against a remote <strong>Kubernetes</strong>
      cluster. The table below sets them beside plug, row by row. What it says about them comes from
      their public documentation, read in October 2026; both move quickly, so check a row that
      matters to you against their current docs.
    </p>
    <p>
      One difference shapes several rows. To receive traffic from the cluster, mirrord and
      Telepresence attach to a workload that is already deployed. plug publishes a name instead:
      <code>plug -s my-api:8080:3000 npm run start:dev</code> puts <code>my-api</code> in the
      cluster for the session, whether a <code>my-api</code> was deployed or not. When one is, plug
      sets it aside and restores it on exit.
    </p>
    <div class="cmp">
      <table>
        <thead>
          <tr><th scope="col"><span class="sr-only">Criterion</span></th><th scope="col">plug</th><th scope="col">mirrord</th><th scope="col">Telepresence</th></tr>
        </thead>
        <tbody>
          <tr><th scope="row">Cluster targets</th><td class="win">Docker Compose · Swarm · Kubernetes · OpenShift / OKD</td><td>Kubernetes (OpenShift after SCC changes, per its FAQ)</td><td>Kubernetes · OpenShift</td></tr>
          <tr><th scope="row">Your machine</th><td class="win">Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64, arm64</td><td>Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64</td><td class="win">Linux - amd64, arm64<br />macOS - amd64, arm64<br />Windows - amd64, arm64</td></tr>
          <tr><th scope="row">What a developer needs</th><td class="win">the cluster's address: plug installs itself from it</td><td>a kubeconfig with rights to start its agent pod, or the Operator (paid plans)</td><td>a kubeconfig; little or no RBAC once an administrator installed the traffic-manager</td></tr>
          <tr><th scope="row">To receive cluster traffic</th><td class="win">a name, which need not exist in the cluster, and can take over one that does</td><td>an existing workload, named with <code>--target</code> (targetless mode: outgoing traffic only)</td><td>an existing workload, attached with <code>replace</code>, <code>intercept</code> or <code>wiretap</code></td></tr>
          <tr><th scope="row">Setup, your machine</th><td>one ssh command, served by the cluster, at the version its agent runs</td><td>brew, curl, choco or an MSI</td><td>a package, an installer or brew</td></tr>
          <tr><th scope="row">Staying up to date</th><td>the launcher runs the version each cluster's agent serves, one cached core per version; <code>plug update</code> moves the agent</td><td>reinstall the CLI; the Operator, where used, is upgraded separately</td><td>reinstall the CLI; the traffic-manager is upgraded separately, with Helm</td></tr>
          <tr><th scope="row">Setup, cluster side</th><td>one agent container</td><td class="win">none: an agent pod per session (the Operator on paid plans)</td><td>the traffic-manager, installed once</td></tr>
          <tr><th scope="row">Reach cluster services by name</th><td>✓</td><td>✓</td><td>✓</td></tr>
          <tr><th scope="row">Be reachable by a cluster name</th><td class="win">✓ <code>-s name:8080:3000</code>: the name is created for the session, no existing service needed</td><td>by stealing or mirroring an existing workload's traffic</td><td>by replacing or intercepting an existing workload</td></tr>
          <tr><th scope="row">Runtimes</th><td class="win">any, unchanged (IP layer)</td><td>programs linked against libc, and Go; not statically linked binaries</td><td class="win">any, unchanged (network level)</td></tr>
          <tr><th scope="row">Run a container as a member</th><td>✓ <code>--dockerrun</code></td><td>✓ <code>mirrord container</code></td><td>✓ <code>--docker-run</code></td></tr>
          <tr><th scope="row">That container's <code>-p</code> and <code>--network</code></th><td class="win">✓ as on any container: given to the sidecar that owns the network, the line read with the options your docker CLI lists</td><td>a warning on <code>-p</code>; ports go in the mirrord config (<code>container.cli_extra_args</code>)</td><td class="win">✓ since 2.21 in Docker mode (<code>--publish</code>, <code>--expose</code>, <code>--network</code>)</td></tr>
          <tr><th scope="row">Inherit the workload's environment</th><td>✓ by default on a takeover; the cluster's value wins over an inherited one, and <code>--no-env</code> keeps yours, whole or key by key</td><td>✓ by default, from the target</td><td>✓ on any attach</td></tr>
          <tr><th scope="row">Borrow another workload's environment</th><td class="win">✓ <code>--env-of &lt;name&gt;</code>, without parking it</td><td>only the <code>--target</code></td><td class="win">✓ <code>telepresence ingest &lt;workload&gt;</code>, traffic untouched</td></tr>
          <tr><th scope="row">Its mounted secret and config files</th><td>✓ read through the agent and written for the session, the variables that name them repointed; Kubernetes and Compose secrets, Swarm configs</td><td>✓ file calls relayed into the pod by its in-process layer; remote reads by default, read-write configured per path</td><td>✓ volume mounts through sshfs: FUSE-T on macOS, WinFsp and SSHFS-Win on Windows; none in Docker mode</td></tr>
          <tr><th scope="row">Its data volumes, live</th><td class="win">✓ by default on a takeover, read-write, with the SMB client the OS already has; nothing to install</td><td>✓ through its in-process file layer</td><td>✓ mounted by default, through sshfs (FUSE-T or WinFsp to install)</td></tr>
          <tr><th scope="row">Several developers on one cluster</th><td class="win">✓ each with their own sessions. A name already served is refused, and the refusal says where it is held from</td><td>✓ on the paid Team plan; the free version is for one developer</td><td class="win">✓</td></tr>
          <tr><th scope="row">Two developers on the same service at once</th><td>one holder per name: the next one is refused</td><td class="win">split per request on HTTP headers, path or method, or a queue message attribute; concurrent use on the paid Team plan</td><td class="win">split per request on a header or a path (HTTP)</td></tr>
          <tr><th scope="row">Who gets in</th><td>open by default; by approval with <code>PLUG_ADMISSION=invite</code>, each key accepted by a member and revocable, no account in the cluster (<a routerLink="/security" fragment="admission">how</a>)</td><td>whoever holds a kubeconfig the cluster issued</td><td>whoever the cluster lets reach its traffic-manager</td></tr>
          <tr><th scope="row">What each person may do</th><td>every member, everything plug does; per-person rights through a gateway such as <a routerLink="/meerkat">Meerkat</a></td><td class="win">Kubernetes RBAC; Operator policies on paid plans</td><td class="win">Kubernetes RBAC</td></tr>
          <tr><th scope="row">IDE extensions</th><td>none, CLI only</td><td class="win">VS Code · JetBrains</td><td>JetBrains (a JetBrains plugin)</td></tr>
          <tr><th scope="row">For an AI coding agent</th><td><a routerLink="/mcp"><code>plug mcp</code></a>: the clusters a machine knows, <code>doctor</code>, a workload's environment, DNS lookups</td><td><code>mirrord mcp</code>, plus agent skills</td><td><code>telepresence mcp</code></td></tr>
          <tr><th scope="row">Mechanism</th><td>userspace TUN over SSH, an agent in the cluster</td><td>libc and syscall hooks loaded into your process, an agent pod in the cluster</td><td>a virtual network interface on your machine, a traffic-manager and traffic-agents in the cluster</td></tr>
          <tr><th scope="row">Behind it</th><td>Softwarity</td><td>MetalBear</td><td>a CNCF Sandbox project, created by Ambassador Labs</td></tr>
          <tr><th scope="row">Price</th><td class="win">free, every feature</td><td>free for one developer; the Team plan, per seat, for a shared cluster</td><td class="win">free</td></tr>
          <tr><th scope="row">License</th><td>FSL-1.1-Apache-2.0</td><td>MIT</td><td>Apache-2.0</td></tr>
        </tbody>
      </table>
    </div>
    <p class="cmp-legend">Green marks whichever tool is ahead on the fact stated in that row, whichever it is, and every one of them when several are. Rows where all three are level are left plain.</p>
    <p class="note">
      <strong>Where plug differs:</strong> a developer needs the cluster's address and nothing else:
      no kubeconfig, no Kubernetes tooling, no account in the cluster. That is also why it works
      where there is no kubeconfig at all: Docker Compose and Swarm. And the name it serves does not
      have to exist yet: the service you are writing, deployed nowhere, is reachable from the rest
      of the stack by its future name.
    </p>
    <p class="note">
      <strong>Where they are ahead:</strong> both rest on Kubernetes RBAC, which says per person
      what each one may do; plug decides who gets in, not what each member may do once in (see the
      <a routerLink="/security">security model</a>). Both split one shared service between several
      developers request by request, provided the header is set where the request enters and every
      service in between passes it on. And both have IDE extensions.
    </p>
    <app-cta link="/getting-started">Set it up</app-cta>
  `,
})
export class ComparisonComponent {}
