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
      .diagram svg {
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
      <strong>full member of your cluster</strong> — in both directions. Your process reaches
      cluster services by their real names, and it is itself reachable from inside the cluster under
      a name. No code change, no proxy configuration — you just prefix your usual command:
    </p>

    <app-code lang="text">plug -s my-app:8080:3000 npm run start:dev</app-code>

    <div class="diagram">
      <svg viewBox="0 0 900 511" xmlns="http://www.w3.org/2000/svg" role="img" font-family="-apple-system, Segoe UI, Roboto, sans-serif">
        <title>plug: your process is a full member of the cluster, both directions</title>
        <desc>Animated in two phases. First, the cluster runs on its own: the browser reaches the gateway, the DNS routes service1 to the deployed service, which queries postgres. Then plug -s starts on your machine: the deployed service1 is parked (disabled), the DNS routes service1 to the plug agent, and the same flows now go through the SSH tunnel to your local process — which queries postgres by name in return. The loop then restarts.</desc>
        <defs>
          <marker id="ab-g" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="#3fb950" /></marker>
          <marker id="ab-p" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="#a371f7" /></marker>
        </defs>
        <rect x="2" y="2" width="896" height="507" rx="16" fill="#0d1117" stroke="#30363d" stroke-width="1.5" />

        <rect x="30" y="180" width="132" height="102" rx="10" fill="#161b22" stroke="#30363d" />
        <rect x="42" y="192" width="108" height="18" rx="4" fill="#21262d" />
        <circle cx="51" cy="201" r="2.3" fill="#484f58" /><circle cx="60" cy="201" r="2.3" fill="#484f58" /><circle cx="69" cy="201" r="2.3" fill="#484f58" />
        <rect x="42" y="214" width="108" height="50" fill="#0d1117" stroke="#30363d" />
        <text x="96" y="244" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="11" fill="#e6edf3">app.acme.com</text>
        <text x="96" y="304" text-anchor="middle" font-size="12" fill="#8b949e">a browser, out there</text>

        <rect x="222" y="52" width="418" height="396" rx="12" fill="#0e141b" stroke="#30363d" />
        <text x="246" y="80" font-size="12" letter-spacing="2" fill="#8b949e" font-weight="700">THE CLUSTER</text>
        <rect x="248" y="104" width="104" height="50" rx="8" fill="#161b22" stroke="#30363d" />
        <text x="300" y="126" text-anchor="middle" font-size="12.5" fill="#e6edf3" font-weight="600">gateway</text>
        <text x="300" y="143" text-anchor="middle" font-size="9" fill="#6e7681">published :443</text>
        <rect x="248" y="192" width="132" height="54" rx="8" fill="#161b22" stroke="#30363d" />
        <text x="314" y="214" text-anchor="middle" font-size="12" fill="#e6edf3" font-weight="600">cluster DNS</text>

        <rect x="520" y="146" width="90" height="98" rx="8" fill="#161b22" stroke="#a371f7" stroke-width="1.4" />
        <text x="565" y="182" text-anchor="middle" font-size="12.5" fill="#e6edf3" font-weight="600">plug</text>
        <text x="565" y="199" text-anchor="middle" font-size="11" fill="#8b949e">agent</text>

        <ellipse cx="574" cy="308" rx="36" ry="10" fill="#161b22" stroke="#30363d" />
        <path d="M538 308 V352 a36 10 0 0 0 72 0 V308" fill="#161b22" stroke="#30363d" />
        <ellipse cx="574" cy="308" rx="36" ry="10" fill="#161b22" stroke="#30363d" />
        <text x="574" y="338" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="12.5" fill="#e6edf3" font-weight="600">postgres</text>

        <!-- BEFORE plug (phase 1): the deployed service1 is live and serves the flows. -->
        <g opacity="1">
          <animate attributeName="opacity" values="1;1;0;0;1" keyTimes="0;0.42;0.5;0.92;1" dur="12s" repeatCount="indefinite" />
          <rect x="478" y="390" width="132" height="40" rx="8" fill="#21262d" stroke="#30363d" />
          <text x="544" y="408" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="12" fill="#e6edf3" font-weight="600">service1</text>
          <text x="544" y="422" text-anchor="middle" font-size="8" fill="#8b949e">deployed in the stack</text>
          <path d="M314 246 V410 H472" fill="none" stroke="#3fb950" stroke-width="2.2" marker-end="url(#ab-g)" />
          <line x1="574" y1="387" x2="574" y2="366" stroke="#a371f7" stroke-width="2.2" marker-end="url(#ab-p)" />
          <text x="562" y="380" text-anchor="end" font-size="9.5" fill="#a371f7">query postgres by name</text>
        </g>

        <!-- WITH plug (phase 2): service1 is parked, the flows go to your machine. -->
        <g opacity="0">
          <animate attributeName="opacity" values="0;0;1;1;0" keyTimes="0;0.42;0.5;0.92;1" dur="12s" repeatCount="indefinite" />
          <text x="314" y="232" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="9.5" fill="#3fb950">service1 → agent</text>
          <g opacity="0.55">
            <rect x="478" y="390" width="132" height="40" rx="8" fill="#161b22" stroke="#30363d" stroke-dasharray="4 3" />
            <text x="544" y="408" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="11.5" fill="#8b949e" font-weight="600">service1</text>
            <text x="544" y="422" text-anchor="middle" font-size="8" fill="#6e7681">deployed · restored on exit</text>
          </g>
          <rect x="564" y="383" width="42" height="14" rx="7" fill="#0d1117" stroke="#d29922" stroke-width="0.8" />
          <text x="585" y="393" text-anchor="middle" font-size="8.5" fill="#d29922">parked</text>
          <line x1="380" y1="215" x2="518" y2="200" stroke="#3fb950" stroke-width="2.2" marker-end="url(#ab-g)" />
          <line x1="610" y1="173" x2="740" y2="173" stroke="#3fb950" stroke-width="2.2" marker-end="url(#ab-g)" />
          <text x="676" y="167" text-anchor="middle" font-size="9.5" fill="#3fb950">to your machine</text>
          <line x1="740" y1="220" x2="612" y2="220" stroke="#a371f7" stroke-width="2.2" marker-end="url(#ab-p)" />
          <line x1="574" y1="248" x2="574" y2="295" stroke="#a371f7" stroke-width="2.2" marker-end="url(#ab-p)" />
          <text x="562" y="275" text-anchor="end" font-size="9.5" fill="#a371f7">query postgres by name</text>
        </g>

        <!-- Your side: dimmed until the plug session starts. -->
        <g opacity="0.3">
          <animate attributeName="opacity" values="0.3;0.3;1;1;0.3" keyTimes="0;0.42;0.5;0.92;1" dur="12s" repeatCount="indefinite" />
          <rect x="640" y="146" width="86" height="98" rx="6" fill="#0d1117" stroke="#21262d" stroke-dasharray="3 3" />
          <text x="683" y="140" text-anchor="middle" font-size="9" fill="#6e7681">SSH tunnel</text>
          <rect x="726" y="118" width="152" height="164" rx="12" fill="#161b22" stroke="#30363d" />
          <text x="742" y="140" font-size="11" letter-spacing="1.5" fill="#8b949e" font-weight="700">YOUR MACHINE</text>
          <rect x="742" y="150" width="120" height="64" rx="8" fill="#21262d" stroke="#30363d" />
          <text x="802" y="176" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="14" fill="#e6edf3" font-weight="600">service1</text>
          <text x="802" y="196" text-anchor="middle" font-size="10" fill="#8b949e">npm run start:dev</text>
          <text x="802" y="254" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="9.5" fill="#6e7681">plug -s service1:80:3000</text>
          <rect x="502" y="100" width="386" height="190" rx="16" fill="none" stroke="#a371f7" stroke-width="1.3" stroke-dasharray="6 4" opacity="0.85" />
          <rect x="502" y="90" width="48" height="20" rx="5" fill="#0d1117" stroke="#a371f7" />
          <text x="526" y="104" text-anchor="middle" font-size="11" fill="#a371f7" font-weight="700">plug</text>
        </g>

        <line x1="162" y1="226" x2="246" y2="150" stroke="#3fb950" stroke-width="2.2" marker-end="url(#ab-g)" />
        <text x="188" y="174" text-anchor="middle" font-size="10.5" fill="#3fb950" font-weight="600" transform="rotate(-30 188 174)">GET</text>
        <line x1="300" y1="154" x2="300" y2="190" stroke="#3fb950" stroke-width="2" marker-end="url(#ab-g)" />

        <line x1="60" y1="481" x2="90" y2="481" stroke="#3fb950" stroke-width="2.6" />
        <text x="98" y="485" font-size="11.5" fill="#8b949e">inbound — browser → gateway → DNS → agent → your process (plug -s)</text>
        <line x1="560" y1="481" x2="590" y2="481" stroke="#a371f7" stroke-width="2.6" />
        <text x="598" y="485" font-size="11.5" fill="#8b949e">outbound — your process → postgres</text>
      </svg>
    </div>
    <p class="cap">
      One SSH tunnel carries both directions: your process reaches the cluster by name, and the
      cluster reaches your process by the name you serve with <code>-s</code>. Already deployed?
      plug <strong>parks</strong> the in-cluster <code>service1</code> for the session — your
      process takes its place, and it is restored when the session ends.
    </p>

    <div class="dirs">
      <div class="dir">
        <span class="dir-tag out">outbound</span><br />
        <strong>Reach the cluster by name.</strong> Your process addresses
        <code>postgres</code>, <code>my-service:8080</code> — the same names any workload inside
        uses. No port-forwards, no <code>localhost:PORT</code> mappings.
      </div>
      <div class="dir">
        <span class="dir-tag in">inbound</span><br />
        <strong>Be reachable by name.</strong> With <code>-s</code>, a name you serve is reachable
        from inside the cluster — a gateway, another service, or a browser through the ingress lands
        on your machine. No name pre-declared, no redeploy.
      </div>
    </div>

    <div class="callout">
      <strong>Two pieces.</strong> A tiny <a routerLink="/swarm">agent container</a> (Alpine + sshd)
      deployed once on the cluster — and a single static <code>plug</code> binary on each dev
      machine. Set up once per cluster; after that, day-to-day runs need no sudo or admin.
    </div>

    <h3>What you get</h3>
    <section class="features">
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">dns</mat-icon>
        <span class="feature-title">Names, resolved cluster-side</span>
        <span class="feature-desc">Address <code>my-service:8080</code> by its real name — no <code>localhost:PORT</code> mappings, no <code>/etc/hosts</code> edits.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">all_inclusive</mat-icon>
        <span class="feature-title">Every runtime, unchanged</span>
        <span class="feature-desc">Traffic is captured at the IP layer, so your app's socket is never touched — Node, the JVM, Python, <strong>Go</strong>, curl, gRPC, DB drivers all just work.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">swap_horiz</mat-icon>
        <span class="feature-title">Reachable from the cluster</span>
        <span class="feature-desc"><code>-s</code> publishes a local port under a cluster name — a gateway or workload reaches your process, for the session. A deployed service owning the name is <strong>parked</strong> meanwhile, restored on exit.</span>
      </a>
      <a routerLink="/profiles" class="feature-card">
        <mat-icon class="feature-icon">hub</mat-icon>
        <span class="feature-title">Several clusters at once</span>
        <span class="feature-desc">Run the same process against two clusters in parallel — each session stays isolated.</span>
      </a>
      <a routerLink="/swarm" class="feature-card">
        <mat-icon class="feature-icon">devices</mat-icon>
        <span class="feature-title">Linux · macOS · Windows</span>
        <span class="feature-desc">Native on all three (no WSL2 needed); a multi-arch <code>amd64</code>/<code>arm64</code> agent image.</span>
      </a>
      <a routerLink="/security" class="feature-card">
        <mat-icon class="feature-icon">shield</mat-icon>
        <span class="feature-title">Honest security model</span>
        <span class="feature-desc">Deliberately auth-less, for trusted dev clusters — read the model before deploying.</span>
      </a>
    </section>

    <h3>How plug compares</h3>
    <p>
      plug isn't the only way to run local code against a remote cluster —
      <a href="https://metalbear.com/mirrord/" target="_blank" rel="noopener">mirrord</a> and
      <a href="https://telepresence.io/" target="_blank" rel="noopener">Telepresence</a> are the
      well-known <strong>Kubernetes-native</strong> tools, more mature on Kubernetes and on team
      workflows. plug's angle is different: it works the same on Docker, Compose, Swarm
      <em>and</em> Kubernetes, it is fully open source, and it is deliberately simple — and
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
          <tr><td>Auth</td><td>none — trusted dev cluster</td><td>your kubeconfig / RBAC</td><td>your kubeconfig / RBAC</td></tr>
          <tr><td>Per-dev isolation on a shared service</td><td>one name, one session</td><td>Operator (header / queue split)</td><td>intercept filtering (header / path)</td></tr>
          <tr><td>Price</td><td><strong>Free · AGPL-3.0</strong></td><td>Free OSS · $40/seat/mo Teams · Enterprise custom</td><td>Free OSS · paid cloud features</td></tr>
        </tbody>
      </table>
    </div>
    <p class="cmp-note">
      <strong>Why plug:</strong> it brings the cluster onto your own machine — you build your service
      exactly as if it lived inside the stack, calling the others by their real names and answering
      to its own name when they call back, with no code change. And, above all,
      <strong>it works the same everywhere</strong>: Docker, Compose, Swarm <em>and</em> Kubernetes,
      on Linux, macOS and Windows — where mirrord and Telepresence stop at Kubernetes. Fully open
      source, no paid tier, just one tiny agent — and because it captures at the IP layer, every
      runtime works unchanged, Go and gRPC included.
    </p>

    <p class="cta">
      <a routerLink="/getting-started">Set it up <mat-icon aria-hidden="true" style="font-size:18px;width:18px;height:18px">arrow_forward</mat-icon></a>
    </p>
  `,
})
export class AboutComponent {}
