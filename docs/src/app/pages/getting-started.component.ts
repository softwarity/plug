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
      <strong>Two pieces.</strong> A tiny <a routerLink="/agent">agent container</a> (Alpine + sshd)
      deployed once on the cluster — and a single static <code>plug</code> binary on each dev
      machine. No root, no daemon; the proxy lives exactly as long as your command.
    </div>

    <h3>What you get</h3>
    <section class="features">
      <a routerLink="/how-it-works" class="feature-card">
        <span class="feature-icon">🔌</span>
        <span class="feature-title">Names, resolved cluster-side</span>
        <span class="feature-desc">Address <code>my-service:8080</code> by name (via <code>socks5h</code>) — no <code>localhost:PORT</code> mappings.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <span class="feature-icon">🪶</span>
        <span class="feature-title">No root, no daemon</span>
        <span class="feature-desc">A userspace SOCKS proxy + env vars. Nothing global is touched on your machine.</span>
      </a>
      <a routerLink="/profiles" class="feature-card">
        <span class="feature-icon">🧬</span>
        <span class="feature-title">Multi-cluster at once</span>
        <span class="feature-desc">Run the same process against two clusters in parallel — each session is isolated.</span>
      </a>
      <a routerLink="/profiles" class="feature-card">
        <span class="feature-icon">🔀</span>
        <span class="feature-title">Port-forwards</span>
        <span class="feature-desc">Raw-TCP drivers that ignore the proxy (AMQP…) get a per-session local port + injected env.</span>
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
      Add the service to the stack you want to plug into — it joins the stack's network
      automatically:
    </p>
    <app-code lang="yaml"># your existing stack file
services:
  plug-agent:
    image: docker.io/softwarity/plug-agent:latest
    ports:
      - "2222:22"
    environment:
      - PLUG_PUBLIC_HOST=swarm.example.com   # the address devs connect to</app-code>
    <p>
      <code>PLUG_PUBLIC_HOST</code> is the address devs reach the agent on; it is baked into their
      profile at install time, so the install needs no repetition. See
      <a routerLink="/agent">Agent &amp; deployment</a> for the standalone and Kubernetes variants.
    </p>

    <h3>2. Install the CLI (each dev machine)</h3>
    <p>
      One line, straight from the cluster — the agent serves an installer that downloads the right
      binary (<code>uname</code>-detected), puts it on your <code>PATH</code> and writes a default
      profile pointing at <code>PLUG_PUBLIC_HOST</code>. No GitHub access, no root:
    </p>
    <app-code lang="bash">ssh -p 2222 get@&lt;cluster-host&gt; install | sh</app-code>
    <p>
      No key, no password — a passwordless <code>get</code> user locked to a single "hand me a
      binary" command (see <a routerLink="/security">Security model</a>). Prefer GitHub? The same
      binaries are attached to every
      <a href="https://github.com/softwarity/plug/releases" target="_blank" rel="noopener">release</a>.
      That is the whole install — <strong>a single static binary, no other dependency, no root</strong>.
    </p>

    <h3>3. Run your process against the cluster</h3>
    <app-code lang="bash">plug npm run start:dev</app-code>
    <p>
      First run: a <a routerLink="/profiles">short wizard</a> asks for the cluster host and port
      (default <code>2222</code>) and saves a profile. Then plug opens a local SOCKS5 proxy, points
      your command's environment at it, and runs it. In your code you address services by name —
      <code>http://pdfbox:8080</code>, <code>mongodb:27017</code> — and they resolve inside the
      cluster. <kbd>Ctrl-C</kbd> stops your process and closes the proxy. No sudo, ever.
    </p>
    <p>
      plug is a small <strong>launcher</strong>: on connect it asks the agent which version it
      speaks and runs <em>exactly that version</em> (cached under <code>~/.plug/versions/</code>,
      downloaded once). Each cluster runs its own matching version, so several clusters on different
      versions never conflict. See <a routerLink="/profiles">Profiles &amp; versions</a>.
    </p>

    <h3>Compatibility</h3>
    <ul>
      <li>CLI: <strong>macOS</strong>, <strong>Linux</strong> and <strong>Windows</strong> natively — a single static binary, nothing else to install.</li>
      <li>Cluster: <strong>Docker Swarm</strong> today — <a routerLink="/roadmap">Kubernetes is on the roadmap</a>.</li>
      <li>Runtime-agnostic: NestJS, Spring, Quarkus, curl… anything that honors the proxy env (and <a routerLink="/profiles">port-forwards</a> cover the rest).</li>
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
