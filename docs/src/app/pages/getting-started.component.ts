import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { MatIconModule } from '@angular/material/icon';

@Component({
  selector: 'app-getting-started',
  imports: [CodeComponent, RouterLink, MatIconModule],
  styles: [
    `
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
    `,
  ],
  template: `
    <h2>Getting started</h2>

    <p>
      <strong>plug</strong> lets a process on your machine behave as if it ran
      <strong>inside your cluster</strong>: cluster DNS names resolve, cluster services are
      reachable. No code change, no proxy configuration in your app — you just prefix your usual
      command:
    </p>

    <app-code lang="text">plug npm run start:dev   ──►   http://my-service:8080 answers, like from any workload in the cluster</app-code>

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
        <mat-icon class="feature-icon">lock_open</mat-icon>
        <span class="feature-title">No sudo day-to-day</span>
        <span class="feature-desc">The privilege plug needs is granted <strong>once at install</strong>; every later <code>plug &lt;cmd&gt;</code> runs with none.</span>
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

    <h3>1. Deploy the agent (once, on the cluster)</h3>
    <p>
      Add the service to the stack you want to reach — it joins the stack's network automatically:
    </p>
    <app-code lang="yaml"># your existing stack file
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports:
      - "2222:22"
    # optional — lets the agent create your -s name on the fly
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    # Swarm only, for -s: the signpost is a service, so run the agent on a
    # manager (any single-node swarm node IS a manager) as a single replica.
    # Ignored by plain Compose.
    deploy:
      replicas: 1
      placement:
        constraints: [node.role == manager]
    <p>
      The socket line is <strong>opt-in</strong>: it lets the agent create your
      <a routerLink="/swarm"><code>-s</code> name</a> on the fly. Without it, you pre-declare the
      name yourself (a network alias, or a Service on Kubernetes). See
      <a routerLink="/swarm">Agent &amp; Swarm</a> for the standalone variant, or
      <a routerLink="/kubernetes">Agent &amp; Kubernetes</a> for the cluster.
    </p>

    <h3>2. Install the CLI (each dev machine)</h3>
    <p>
      One line, straight from the cluster — the agent hands over the right binary. The install
      grants plug its privilege <strong>once</strong> (a single sudo, or a single elevated step on
      Windows) so that no later run ever needs it.
    </p>
    <p><strong>Linux and macOS:</strong></p>
    <app-code lang="bash"># the agent regenerates its host key each start (not a secret here) — skip the check
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@&lt;cluster-host&gt; install | sh</app-code>
    <p><strong>Windows</strong>, from Git Bash (the assumed Windows shell — it ships with
      <a href="https://git-scm.com/download/win" target="_blank" rel="noopener">Git for Windows</a>):</p>
    <app-code lang="bash">cluster=&lt;cluster-host&gt;
ssh -n -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@$cluster install-windows \\
  | bash -s -- $cluster 2222</app-code>
    <p>
      The installer reads the cluster address straight from <em>your</em> <code>ssh</code> command
      and saves a <a routerLink="/profiles">profile named after that host</a>, so plug is ready right
      away. Install from a second cluster and you get a second profile to run alongside. (No live
      <code>ssh</code>? The <a routerLink="/profiles">first run</a> asks once, via a short wizard.)
    </p>

    <h3>3. Run your process against the cluster</h3>
    <app-code lang="bash">plug -s my-app:8080:3000 npm run start:dev</app-code>
    <p>
      plug runs your command as a named member of the cluster: it answers to
      <code>my-app:8080</code> (forwarded to its local <code>:3000</code>), and in your code you
      address cluster services by name — <code>http://pdfbox:8080</code>, <code>mongodb:27017</code>
      — which resolve inside the cluster. <kbd>Ctrl-C</kbd> stops your process; when the last one
      exits, your machine is back exactly as it was. No sudo, no admin.
    </p>
    <p>
      plug is a small <strong>launcher</strong>: on connect it asks the agent which version it
      speaks and runs <em>exactly that version</em> (cached under <code>~/.plug/versions/</code>,
      downloaded once). Each cluster runs its own matching version, so several clusters on different
      versions never conflict. See <a routerLink="/profiles">Profiles &amp; versions</a>.
    </p>

    <h3>Where to next</h3>
    <p>
      Read <a routerLink="/how-it-works">How it works</a> for the mechanics,
      <a routerLink="/profiles">Profiles &amp; versions</a> for day-to-day usage,
      <a routerLink="/swarm">Agent &amp; Swarm</a> for the cluster side,
      <a routerLink="/security">Security model</a> before deploying anywhere sensitive, the
      <a routerLink="/coverage">Coverage matrix</a> for what works on which OS, and the
      <a routerLink="/roadmap">Roadmap</a> for what's coming.
    </p>
  `,
})
export class GettingStartedComponent {}
