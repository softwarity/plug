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
      <svg viewBox="0 0 900 470" xmlns="http://www.w3.org/2000/svg" role="img" font-family="-apple-system, Segoe UI, Roboto, sans-serif">
        <title>plug: your process is a full member of the cluster, both directions</title>
        <desc>A browser hits the cluster's public gateway; the gateway calls service1, the cluster DNS resolves it to the plug agent, which tunnels to your machine's process; that process queries postgres by name back through the agent.</desc>
        <defs>
          <marker id="ab-g" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="#3fb950" /></marker>
          <marker id="ab-p" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10 z" fill="#a371f7" /></marker>
        </defs>
        <rect x="2" y="2" width="896" height="466" rx="16" fill="#0d1117" stroke="#30363d" stroke-width="1.5" />

        <rect x="30" y="196" width="132" height="102" rx="10" fill="#161b22" stroke="#30363d" />
        <rect x="42" y="208" width="108" height="18" rx="4" fill="#21262d" />
        <circle cx="51" cy="217" r="2.3" fill="#484f58" /><circle cx="60" cy="217" r="2.3" fill="#484f58" /><circle cx="69" cy="217" r="2.3" fill="#484f58" />
        <rect x="42" y="230" width="108" height="50" fill="#0d1117" stroke="#30363d" />
        <text x="96" y="260" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="11" fill="#e6edf3">app.acme.com</text>
        <text x="96" y="320" text-anchor="middle" font-size="12" fill="#8b949e">a browser, out there</text>

        <rect x="222" y="52" width="418" height="356" rx="12" fill="#0e141b" stroke="#30363d" />
        <text x="246" y="80" font-size="12" letter-spacing="2" fill="#8b949e" font-weight="700">THE CLUSTER</text>
        <rect x="248" y="104" width="104" height="50" rx="8" fill="#161b22" stroke="#30363d" />
        <text x="300" y="126" text-anchor="middle" font-size="12.5" fill="#e6edf3" font-weight="600">gateway</text>
        <text x="300" y="143" text-anchor="middle" font-size="9" fill="#6e7681">published :443</text>
        <rect x="248" y="192" width="132" height="54" rx="8" fill="#161b22" stroke="#30363d" />
        <text x="314" y="214" text-anchor="middle" font-size="12" fill="#e6edf3" font-weight="600">cluster DNS</text>
        <text x="314" y="232" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="9.5" fill="#3fb950">service1 → agent</text>
        <ellipse cx="304" cy="312" rx="36" ry="10" fill="#161b22" stroke="#30363d" />
        <path d="M268 312 V356 a36 10 0 0 0 72 0 V312" fill="#161b22" stroke="#30363d" />
        <ellipse cx="304" cy="312" rx="36" ry="10" fill="#161b22" stroke="#30363d" />
        <text x="304" y="342" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="12.5" fill="#e6edf3" font-weight="600">postgres</text>
        <rect x="520" y="178" width="90" height="98" rx="8" fill="#161b22" stroke="#a371f7" stroke-width="1.4" />
        <text x="565" y="214" text-anchor="middle" font-size="12.5" fill="#e6edf3" font-weight="600">plug</text>
        <text x="565" y="231" text-anchor="middle" font-size="11" fill="#8b949e">agent</text>

        <rect x="640" y="178" width="86" height="98" rx="6" fill="#0d1117" stroke="#21262d" stroke-dasharray="3 3" />
        <text x="683" y="172" text-anchor="middle" font-size="9" fill="#6e7681">SSH tunnel</text>

        <rect x="726" y="150" width="152" height="164" rx="12" fill="#161b22" stroke="#30363d" />
        <text x="742" y="172" font-size="11" letter-spacing="1.5" fill="#8b949e" font-weight="700">YOUR MACHINE</text>
        <rect x="742" y="182" width="120" height="64" rx="8" fill="#21262d" stroke="#30363d" />
        <text x="802" y="208" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="14" fill="#e6edf3" font-weight="600">service1</text>
        <text x="802" y="228" text-anchor="middle" font-size="10" fill="#8b949e">npm run start:dev</text>
        <text x="802" y="286" text-anchor="middle" font-family="ui-monospace, Menlo, monospace" font-size="9.5" fill="#6e7681">plug -s service1:80:3000</text>

        <rect x="502" y="132" width="386" height="196" rx="16" fill="none" stroke="#a371f7" stroke-width="1.3" stroke-dasharray="6 4" opacity="0.85" />
        <rect x="502" y="122" width="48" height="20" rx="5" fill="#0d1117" stroke="#a371f7" />
        <text x="526" y="136" text-anchor="middle" font-size="11" fill="#a371f7" font-weight="700">plug</text>

        <line x1="162" y1="242" x2="246" y2="150" stroke="#3fb950" stroke-width="2.2" marker-end="url(#ab-g)" />
        <text x="188" y="185" text-anchor="middle" font-size="10.5" fill="#3fb950" font-weight="600" transform="rotate(-30 188 185)">GET</text>
        <line x1="300" y1="154" x2="300" y2="190" stroke="#3fb950" stroke-width="2" marker-end="url(#ab-g)" />
        <line x1="380" y1="218" x2="518" y2="221" stroke="#3fb950" stroke-width="2.2" marker-end="url(#ab-g)" />
        <line x1="610" y1="205" x2="740" y2="205" stroke="#3fb950" stroke-width="2.2" marker-end="url(#ab-g)" />
        <text x="676" y="199" text-anchor="middle" font-size="9.5" fill="#3fb950">to your machine</text>

        <line x1="740" y1="248" x2="612" y2="248" stroke="#a371f7" stroke-width="2.2" marker-end="url(#ab-p)" />
        <path d="M518 252 Q 420 290 342 320" fill="none" stroke="#a371f7" stroke-width="2" marker-end="url(#ab-p)" />
        <text x="470" y="300" text-anchor="middle" font-size="9.5" fill="#a371f7">query postgres by name</text>

        <line x1="60" y1="430" x2="90" y2="430" stroke="#3fb950" stroke-width="2.6" />
        <text x="98" y="434" font-size="11.5" fill="#8b949e">inbound — browser → gateway → DNS → agent → your process (plug -s)</text>
        <line x1="560" y1="430" x2="590" y2="430" stroke="#a371f7" stroke-width="2.6" />
        <text x="598" y="434" font-size="11.5" fill="#8b949e">outbound — your process → postgres</text>
      </svg>
    </div>
    <p class="cap">
      One SSH tunnel carries both directions: your process reaches the cluster by name, and the
      cluster reaches your process by the name you serve with <code>-s</code>.
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
        <span class="feature-desc"><code>-s</code> publishes a local port under a cluster name — a gateway or workload reaches your process, for the session.</span>
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

    <p class="cta">
      <a routerLink="/getting-started">Set it up <mat-icon aria-hidden="true" style="font-size:18px;width:18px;height:18px">arrow_forward</mat-icon></a>
    </p>
  `,
})
export class AboutComponent {}
