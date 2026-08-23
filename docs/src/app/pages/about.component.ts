import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { MatIconModule } from '@angular/material/icon';

@Component({
  selector: 'app-about',
  imports: [CodeComponent, RouterLink, MatIconModule],
  styles: [
    `
      .diagram {
        margin: 22px 0 6px;
        overflow-x: auto;
      }
      .diagram img {
        display: block;
        width: 100%;
        min-width: 640px;
        height: auto;
      }
      .cap {
        color: var(--text-muted);
        font-size: 0.82rem;
        margin: 4px 2px 26px;
      }

      .dirs {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
        gap: 14px;
        margin: 0 0 28px;
      }
      .dir {
        padding: 14px 16px;
        background-color: var(--bg-secondary);
        border: 1px solid var(--border-color);
        border-radius: 8px;
        font-size: 0.9rem;
        line-height: 1.5;
        color: var(--text-secondary);
      }
      .dir strong {
        color: var(--text-primary);
      }
      .dir-tag {
        display: inline-block;
        font-size: 0.7rem;
        font-weight: 700;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        padding: 2px 8px;
        border-radius: 999px;
        margin-bottom: 8px;
      }
      .dir-tag.out {
        color: #a371f7;
        background: rgba(163, 113, 247, 0.14);
      }
      .dir-tag.in {
        color: #3fb950;
        background: rgba(63, 185, 80, 0.14);
      }

      .features {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
        gap: 12px;
        margin: 0 0 28px 0;
      }
      .feature-card {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 14px 16px;
        background-color: var(--bg-secondary);
        border: 1px solid var(--border-color);
        border-radius: 8px;
        text-decoration: none;
        transition: all 0.15s;
      }
      .feature-card:hover {
        border-color: var(--accent-purple);
        background-color: rgba(163, 113, 247, 0.1);
        text-decoration: none;
        transform: translateY(-1px);
      }
      .feature-icon {
        font-size: 26px;
        width: 26px;
        height: 26px;
        color: var(--accent-purple);
      }
      .feature-title {
        font-weight: 600;
        color: var(--text-primary);
        font-size: 0.95rem;
      }
      .feature-desc {
        color: var(--text-secondary);
        font-size: 0.85rem;
        line-height: 1.45;
      }
      .feature-desc code {
        font-size: 0.85em;
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
    `,
  ],
  template: `
    <h2>What plug does</h2>

    <p>
      <strong>plug</strong> runs a process on your machine as a
      <strong>full member of your cluster</strong> - in both directions. Your process reaches
      cluster services by their real names, and it is itself reachable from inside the cluster under
      a name. No code change, no proxy configuration - you just prefix your usual command:
    </p>

    <app-code lang="text">plug -s my-app:8080:3000 npm run start:dev</app-code>

    <div class="diagram">
      <img src="assets/about-diagram.svg" alt="plug in two animated rounds: the browser's GET data is served by the deployed service1, then plug parks it and the same request is served by your machine - which also queries postgres by name." />
    </div>
    <p class="cap">
      One SSH tunnel carries both directions: your process reaches the cluster by name, and the
      cluster reaches your process by the name you serve with <code>-s</code>. Already deployed?
      plug <strong>parks</strong> the in-cluster <code>service1</code> for the session - your
      process takes its place, and it is restored when the session ends.
    </p>

    <div class="dirs">
      <div class="dir">
        <span class="dir-tag out">outbound</span><br />
        <strong>Reach the cluster by name.</strong> Your process addresses
        <code>postgres</code>, <code>my-service:8080</code> - the same names any workload inside
        uses. No port-forwards, no <code>localhost:PORT</code> mappings. Only consuming (a DB
        tool, a one-off script)? That's <code>plug -c</code>.
      </div>
      <div class="dir">
        <span class="dir-tag in">inbound</span><br />
        <strong>Be reachable by name.</strong> With <code>-s</code>, a name you serve is reachable
        from inside the cluster - a gateway, another service, or a browser through the ingress lands
        on your machine. No name pre-declared, no redeploy.
      </div>
    </div>

    <div class="callout">
      <strong>Two pieces.</strong> A tiny <a routerLink="/swarm">agent container</a> (Alpine, one
      static Go binary) deployed once on the cluster - and a single static <code>plug</code> binary on each dev
      machine. Set up once per cluster; after that, day-to-day runs need no sudo or admin.
    </div>

    <h3>What you get</h3>
    <section class="features">
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">dns</mat-icon>
        <span class="feature-title">Names, resolved cluster-side</span>
        <span class="feature-desc">Address <code>my-service:8080</code> by its real name - no <code>localhost:PORT</code> mappings, no <code>/etc/hosts</code> edits.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">all_inclusive</mat-icon>
        <span class="feature-title">Every runtime, unchanged</span>
        <span class="feature-desc">Traffic is captured at the IP layer, so your app's socket is never touched - Node, the JVM, Python, <strong>Go</strong>, curl, gRPC, DB drivers all just work.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">swap_horiz</mat-icon>
        <span class="feature-title">Reachable from the cluster</span>
        <span class="feature-desc"><code>-s</code> publishes a local port under a cluster name - a gateway or workload reaches your process, for the session. A deployed service owning the name is <strong>parked</strong> meanwhile, restored on exit.</span>
      </a>
      <a routerLink="/profiles" class="feature-card">
        <mat-icon class="feature-icon">hub</mat-icon>
        <span class="feature-title">Several clusters at once</span>
        <span class="feature-desc">Run the same process against two clusters in parallel - each session stays isolated.</span>
      </a>
      <a routerLink="/swarm" class="feature-card">
        <mat-icon class="feature-icon">devices</mat-icon>
        <span class="feature-title">Linux · macOS · Windows</span>
        <span class="feature-desc">Native on all three (no WSL2 needed); a multi-arch <code>amd64</code>/<code>arm64</code> agent image.</span>
      </a>
      <a routerLink="/security" class="feature-card">
        <mat-icon class="feature-icon">shield</mat-icon>
        <span class="feature-title">Honest security model</span>
        <span class="feature-desc">Deliberately auth-less, for trusted dev clusters - read the model before deploying.</span>
      </a>
    </section>

    <h3>How plug compares</h3>
    <p>
      plug isn't the only way to run local code against a remote cluster -
      <a href="https://metalbear.com/mirrord/" target="_blank" rel="noopener">mirrord</a> and
      <a href="https://telepresence.io/" target="_blank" rel="noopener">Telepresence</a> are the
      well-known <strong>Kubernetes-native</strong> tools, more mature on Kubernetes and on team
      workflows. plug's angle is different: it works the same on Docker, Compose, Swarm
      <em>and</em> Kubernetes, it is fully open source, and it is deliberately simple - and
      auth-less, so only for dev clusters you trust.
    </p>
    <div class="cmp">
      <table>
        <thead>
          <tr><th></th><th>plug</th><th>mirrord</th><th>Telepresence</th></tr>
        </thead>
        <tbody>
          <tr><td>Targets</td><td>Docker · Compose · Swarm · Kubernetes</td><td>Kubernetes</td><td>Kubernetes / OpenShift</td></tr>
          <tr><td>Mechanism</td><td>userspace TUN over SSH, by name</td><td>mirrors a remote pod's traffic / env / files into your process</td><td>in-cluster traffic-manager + intercepts</td></tr>
          <tr><td>Both directions</td><td>reach by name + be reachable by name</td><td>steal / mirror incoming + outbound context</td><td>intercept incoming + outbound</td></tr>
          <tr><td>Any runtime, no code change</td><td>✓ (IP layer)</td><td>✓</td><td>✓</td></tr>
          <tr><td>Cluster-side</td><td>one agent container</td><td>none (uses your kubeconfig)</td><td>traffic-manager install</td></tr>
          <tr><td>Auth</td><td>none - trusted dev cluster</td><td>your kubeconfig / RBAC</td><td>your kubeconfig / RBAC</td></tr>
          <tr><td>Per-dev isolation on a shared service</td><td>one name, one session</td><td>Operator (header / queue split)</td><td>intercept filtering (header / path)</td></tr>
          <tr><td>Price</td><td><strong>Free · FSL-1.1</strong></td><td>Free OSS · $40/seat/mo Teams · Enterprise custom</td><td>Free OSS · paid cloud features</td></tr>
        </tbody>
      </table>
    </div>
    <p class="cmp-note">
      <strong>Why plug:</strong> it brings the cluster onto your own machine - you build your service
      exactly as if it lived inside the stack, calling the others by their real names and answering
      to its own name when they call back, with no code change. And, above all,
      <strong>it works the same everywhere</strong>: Docker, Compose, Swarm <em>and</em> Kubernetes,
      on Linux, macOS and Windows - where mirrord and Telepresence stop at Kubernetes. Fully open
      source, no paid tier, just one tiny agent - and because it captures at the IP layer, every
      runtime works unchanged, Go and gRPC included.
    </p>

    <p class="cta">
      <a routerLink="/getting-started">Set it up <mat-icon aria-hidden="true" style="font-size:18px;width:18px;height:18px">arrow_forward</mat-icon></a>
    </p>
  `,
})
export class AboutComponent {}
