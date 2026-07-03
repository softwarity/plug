import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-getting-started',
  imports: [CodeComponent, RouterLink],
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
        font-size: 1.3rem;
        line-height: 1;
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
      <strong>plug</strong> lets a process on your laptop behave as if it ran
      <strong>inside your Docker Swarm cluster</strong>: cluster DNS names resolve, cluster services
      are reachable. No code change, no proxy configuration in your app — you just prefix your usual
      command:
    </p>

    <app-code lang="text">plug npm run start:dev   ──►   http://my-service:8080 answers, like from any container</app-code>

    <div class="callout">
      <strong>Two pieces.</strong> A tiny <a routerLink="/agent">agent container</a> (Alpine + sshd,
      ~15&nbsp;MB) deployed once on the cluster, attached to your overlay networks — and a static
      <code>plug</code> CLI on each dev machine. The tunnel lives exactly as long as your command.
    </div>

    <h3>What you get</h3>
    <section class="features">
      <a routerLink="/how-it-works" class="feature-card">
        <span class="feature-icon">🔌</span>
        <span class="feature-title">Transparent DNS</span>
        <span class="feature-desc">Service names resolve through the cluster resolver — <code>my-service</code>, not <code>localhost:PORT</code> mappings.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <span class="feature-icon">🧭</span>
        <span class="feature-title">Auto-discovery</span>
        <span class="feature-desc">Overlay subnets are read from the agent itself — nobody types a CIDR.</span>
      </a>
      <a routerLink="/profiles" class="feature-card">
        <span class="feature-icon">🪄</span>
        <span class="feature-title">Zero-config wizard</span>
        <span class="feature-desc">First run asks host + port, saves a profile, connects. Next runs are instant.</span>
      </a>
      <a routerLink="/profiles" class="feature-card">
        <span class="feature-icon">🗂️</span>
        <span class="feature-title">Profiles</span>
        <span class="feature-desc">One file per cluster in <code>~/.plug/</code>; automatic selection, <code>-p name</code> to pick.</span>
      </a>
      <a routerLink="/agent" class="feature-card">
        <span class="feature-icon">🧩</span>
        <span class="feature-title">amd64 · arm64</span>
        <span class="feature-desc">Native multi-arch agent image; CLI binaries for linux, macOS and windows.</span>
      </a>
      <a routerLink="/security" class="feature-card">
        <span class="feature-icon">🛡️</span>
        <span class="feature-title">Honest security model</span>
        <span class="feature-desc">Deliberately auth-less, for trusted dev clusters — read the model before deploying.</span>
      </a>
    </section>

    <h3>1. Deploy the agent (once, on the cluster)</h3>
    <p>
      The stack descriptor lives in the repo. Download it, list your overlay networks in the
      <code>networks:</code> section, deploy:
    </p>
    <app-code lang="bash">curl -fsSLO https://raw.githubusercontent.com/softwarity/plug/main/deploy/plug-stack.yml
# edit the networks: section to list your overlay networks
docker stack deploy -c plug-stack.yml plug</app-code>
    <p>See <a routerLink="/agent">Agent &amp; deployment</a> for networks, port and image details.</p>

    <h3>2. Install the CLI (each dev machine)</h3>
    <p>
      plug drives <a href="https://github.com/sshuttle/sshuttle" target="_blank" rel="noopener">sshuttle</a>
      under the hood — install it first, then grab the binary for your platform from the
      <a href="https://github.com/softwarity/plug/releases" target="_blank" rel="noopener">releases page</a>:
    </p>
    <app-code lang="bash">brew install sshuttle        # macOS — linux: apt/dnf install sshuttle
curl -fsSL -o /usr/local/bin/plug \\
  https://github.com/softwarity/plug/releases/latest/download/plug-darwin-arm64
chmod +x /usr/local/bin/plug</app-code>

    <h3>3. Run your process in the cluster</h3>
    <app-code lang="bash">plug npm run start:dev</app-code>
    <p>
      First run: a <a routerLink="/profiles">short wizard</a> asks for the cluster host and port
      (default <code>2222</code>) and saves a profile. Then plug discovers the overlay subnets,
      brings the tunnel up (sudo prompts once — sshuttle needs it for local packet redirection) and
      starts your command. <kbd>Ctrl-C</kbd> stops your process <em>and</em> tears the tunnel down.
    </p>

    <h3>Compatibility</h3>
    <ul>
      <li>CLI: <strong>macOS</strong> (Intel &amp; Apple silicon) and <strong>Linux</strong> natively; <strong>Windows</strong> via WSL2 (sshuttle has no native Windows support).</li>
      <li>Cluster: <strong>Docker Swarm</strong> today — <a routerLink="/roadmap">Kubernetes is on the roadmap</a> (and already works with manual subnets).</li>
      <li>Runtime-agnostic: NestJS, Spring, Quarkus, plain curl… anything that resolves DNS through the system.</li>
    </ul>

    <h3>Where to next</h3>
    <p>
      Read <a routerLink="/how-it-works">How it works</a> for the mechanics,
      <a routerLink="/profiles">Profiles &amp; wizard</a> for day-to-day usage,
      <a routerLink="/agent">Agent &amp; deployment</a> for the cluster side,
      <a routerLink="/security">Security model</a> before deploying anywhere sensitive, and the
      <a routerLink="/roadmap">Roadmap</a> for what's coming.
    </p>
  `,
})
export class GettingStartedComponent {}
