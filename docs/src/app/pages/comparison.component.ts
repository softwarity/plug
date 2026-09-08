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
      plug isn't the only way to run local code against a remote cluster -
      <a href="https://metalbear.com/mirrord/" target="_blank" rel="noopener">mirrord</a> and
      <a href="https://telepresence.io/" target="_blank" rel="noopener">Telepresence</a> are the
      well-known <strong>Kubernetes-native</strong> tools, more mature on Kubernetes and on team
      workflows. plug's angle is different: it works the same on Docker, Compose, Swarm
      <em>and</em> Kubernetes, and it is deliberately simple - and auth-less, so only for dev
      clusters you trust.
    </p>

    <div class="cmp">
      <table>
        <thead>
          <tr><th></th><th scope="col">plug</th><th scope="col">mirrord</th><th scope="col">Telepresence</th></tr>
        </thead>
        <tbody>
          <tr><td>Targets</td><td>Docker · Compose · Swarm · Kubernetes</td><td>Kubernetes</td><td>Kubernetes / OpenShift</td></tr>
          <tr><td>Setup, your machine</td><td><code>ssh get@&lt;cluster&gt; install | sh</code> - the CLI is served BY the cluster, so its version always matches the agent, and it writes your profile for you</td><td>brew / curl / choco, or an IDE extension</td><td>package or installer, or an IDE extension</td></tr>
          <tr><td>What each developer must hold</td><td><strong>nothing but plug</strong> and network access to the agent - no Kubernetes tooling, no account in the cluster</td><td>a kubeconfig granting rights on the cluster, per developer</td><td>a kubeconfig granting rights on the cluster, per developer</td></tr>
          <tr><td>Mechanism</td><td>userspace TUN over SSH, by name</td><td>mirrors a remote pod's traffic / env / files into your process</td><td>in-cluster traffic-manager + intercepts</td></tr>
          <tr><td>Both directions</td><td>reach by name + be reachable by name</td><td>steal / mirror incoming + outbound context</td><td>intercept incoming + outbound</td></tr>
          <tr><td>Run a CONTAINER as a cluster member</td><td><code>--dockerrun</code>: an unmodified image joins the cluster, no Dockerfile change</td><td><code>mirrord container</code> (docker, podman, nerdctl)</td><td><code>--docker-run</code> / <code>--docker</code></td></tr>
          <tr><td>Any runtime, no code change</td><td>✓ (IP layer)</td><td>✓</td><td>✓</td></tr>
          <tr><td>Cluster-side</td><td>one agent container</td><td>none (uses your kubeconfig)</td><td>traffic-manager install</td></tr>
          <tr><td>Auth</td><td>none - trusted dev cluster</td><td>your kubeconfig / RBAC</td><td>your kubeconfig / RBAC</td></tr>
          <tr><td>Per-dev isolation on a shared service</td><td>one name, one session</td><td>Operator (header / queue split)</td><td>intercept filtering (header / path)</td></tr>
          <tr><td>License</td><td><strong>FSL-1.1-Apache-2.0</strong> - source-available, becomes Apache-2.0 two years after each release</td><td>open source, with a paid team tier</td><td>open source, with paid cloud features</td></tr>
        </tbody>
      </table>
    </div>

    <p class="cmp-note">
      <strong>Why plug:</strong> it brings the cluster onto your own machine - you build your service
      exactly as if it lived inside the stack, calling the others by their real names and answering
      to its own name when they call back, with no code change. And, above all,
      <strong>it works the same everywhere</strong>: Docker, Compose, Swarm <em>and</em> Kubernetes,
      on Linux, macOS and Windows - where mirrord and Telepresence stop at Kubernetes. Free, with no
      paid tier and no seat to buy, just one tiny agent - and because it captures at the IP layer,
      every runtime works unchanged, Go and gRPC included.
    </p>

    <p class="cmp-note">
      <strong>Who carries the setup, and what a developer has to hold.</strong> mirrord and
      Telepresence deploy nothing in the cluster, which reads as simpler until you ask what each
      workstation needs: a kubeconfig granting rights on the cluster, for every developer, plus the
      Kubernetes tooling to go with it. That is not a given on a development machine, it is an
      access many companies hand out sparingly, and it does not exist at all on Docker, Compose or
      Swarm. plug puts the setup <em>once</em> in the cluster - one agent container - and asks a
      developer for nothing but plug itself and a route to that agent. The cost is not removed on
      either side; it is carried by the cluster instead of by every desk. The flip side is on the
      Auth row above and it is real: rights you never granted are also rights you cannot revoke or
      audit, which is what <a routerLink="/meerkat">Meerkat</a> puts back.

    <p class="cmp-note">
      <strong>Where they are ahead, and it is worth saying:</strong> both authenticate with your
      kubeconfig and its RBAC, where plug on its own trusts whoever reaches the agent (see the
      <a routerLink="/security">security model</a>, and <a routerLink="/meerkat">Meerkat</a> for
      named identities). mirrord also needs <strong>nothing deployed in the cluster</strong> for
      day-to-day use, where plug asks for one agent container - though that is a cost moved rather
      than removed, see below. Both also isolate several developers on the SAME shared service by routing
      on a header or a path, where a plug name belongs to one session at a time. And both are older,
      with more integrations and a larger community behind them.
    </p>

    <p class="cta">
      <a routerLink="/getting-started">Set it up <mat-icon aria-hidden="true" style="font-size:18px;width:18px;height:18px">arrow_forward</mat-icon></a>
    </p>
  `,
})
export class ComparisonComponent {}
