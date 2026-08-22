import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { FileComponent } from '../file/file.component';
import { MatIconModule } from '@angular/material/icon';

@Component({
  selector: 'app-getting-started',
  imports: [CodeComponent, FileComponent, RouterLink, MatIconModule],
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
      Two pieces: a small <a routerLink="/swarm">agent</a> on the cluster, and the
      <code>plug</code> CLI on each dev machine. Set up once per cluster - after that, day-to-day
      runs need no sudo or admin. New here? See <a routerLink="/">what plug does</a> first.
    </p>

    <h3>1. Deploy the agent (once, on the cluster)</h3>
    <p>
      Add the service to the stack you want to reach - it joins the stack's network automatically:
    </p>
    <app-file src="assets/plug-service.yml" download="plug-service.yml" [initial]="'opened'" [preview]="16" />
    <p>
      The socket line is <strong>required</strong> on Docker, Compose and Swarm: it is how the agent
      creates your <a routerLink="/swarm"><code>-s</code> name</a>, and an agent without it
      <strong>refuses to start</strong> rather than fail on your first <code>-s</code>. It is root on
      the host, so mount it only on a cluster you trust. Kubernetes needs no socket - a Services-only
      RBAC role instead, and the same rule applies. See
      <a routerLink="/swarm">Swarm</a> for the standalone variant, or
      <a routerLink="/kubernetes">Kubernetes</a> for the cluster.
    </p>

    <h3>2. Install the CLI (each dev machine)</h3>
    <p>
      One line, straight from the cluster - the agent hands over the right binary. The install
      grants plug its privilege <strong>once</strong> (a single sudo, or a single elevated step on
      Windows) so that no later run ever needs it.
    </p>
    <p><strong>Linux and macOS:</strong></p>
    <app-code lang="bash"># the agent regenerates its host key each start (not a secret here) - skip the check
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@&lt;cluster-host&gt; install | sh</app-code>
    <p><strong>Windows</strong>, from Git Bash (the assumed Windows shell - it ships with
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
      address cluster services by name - <code>http://pdfbox:8080</code>, <code>mongodb:27017</code>
 - which resolve inside the cluster. <kbd>Ctrl-C</kbd> stops your process; when the last one
      exits, your machine is back exactly as it was. No sudo, no admin.
    </p>
    <p>
      Running something the cluster will <em>never call back</em> - a GUI database tool (DBeaver,
      MongoDB Compass), a one-off script? Declare it a <strong>pure client</strong> with
      <code>-c</code> instead: it reaches services by name, but nothing is named and no port is
      reserved on the agent. One or the other - never both:
    </p>
    <app-code lang="bash">plug -c "/Applications/MongoDB Compass.app/Contents/MacOS/MongoDB Compass"</app-code>
    <h3>Let plug pick the local port</h3>
    <p>
      The cluster port is agreed in advance - it is what other workloads dial. The
      <strong>local</strong> one is nobody's business but yours, and pinning it is what makes two
      projects fight over <code>3000</code>, or the same app refuse to run on two branches at once.
      Name it instead, and plug picks a free one for each session:
    </p>
    <app-code lang="bash">plug -s web:8080:PORT  npm run dev -- --port=&#123;PORT&#125;</app-code>
    <p>
      <code>PORT</code> <strong>declares</strong> it - bare, because the third field of a
      <code>-s</code> can only ever be a port, so there is nothing to disambiguate.
      <code>&#123;PORT&#125;</code> <strong>references</strong> it in the command - braced, because
      argv is free text and a bare <code>PORT</code> would also rewrite
      <code>--transport=PORTAL</code>. Any name works. The mapping is armed on that same number,
      so the cluster reaches <code>web:8080</code> whichever port your process landed on today.
    </p>
    <p>
      The command line is the only channel - plug puts nothing in your process's environment. One
      number, one way to hand it over, and no variable of yours quietly overwritten. Some things
      this makes easy:
    </p>
    <app-code lang="bash"># Two branches of the same service, side by side, both answering in the cluster
git worktree add ../hotfix &amp;&amp; cd ../hotfix
plug -s api:8080:PORT ./mvnw spring-boot:run -Dserver.port=&#123;PORT&#125;

# One process, two cluster names, one listener - the same name means one port
plug -s web:80:PORT -s web-tls:443:PORT node server.js --listen=&#123;PORT&#125;

# A shared CI runner, where a pinned port is a race against the other jobs
plug -s e2e:8080:PORT npm run serve -- --port=&#123;PORT&#125;</app-code>
    <p>
      The two halves have to match, and plug says so at startup rather than let either mistake
      through - both fail silently otherwise, from opposite ends. A
      <code>&#123;TOKEN&#125;</code> nothing declared reaches your command as the literal string
      <code>&#123;PROT&#125;</code>, which either crashes it or makes it fall back to a default
      port the cluster is not forwarding to. A name nothing references allocates a port your
      process is never told about - the cluster name gets published, and nothing ever answers it.
      Commands that use braces for their own purposes (<code>awk '&#123;print&#125;'</code>) are
      untouched when no <code>-s</code> names a port.
    </p>
    <p>
      Pinning still works, and still makes sense when something outside the session needs a stable
      address - a bookmarked URL, a debugger attach config. Naming needs plug ≥ 2.4 on both sides:
      your launcher checks the mapping before it connects, and the cluster's own core is what
      resolves it. <code>plug update</code> aligns the two.
    </p>

    <p>
      Something feels off? <code>plug doctor</code> checks everything plug touches - binaries,
      resolver state, the privileged service, each profile's cluster - and names the remedy next
      to each finding (it can open a pre-filled GitHub issue, redacted, if you want to report one).
    </p>
    <p>
      plug is a small <strong>launcher</strong>: on connect it asks the agent which version it
      speaks and runs <em>exactly that version</em> (cached under <code>~/.plug/versions/</code>,
      downloaded once). Each cluster runs its own matching version, so several clusters on different
      versions never conflict. See <a routerLink="/profiles">Profiles &amp; versions</a>.
    </p>
    <p>
      New plug release? <code>plug update</code> walks that chain upstream: the agent moves
      <em>itself</em> to the newest release, then the launcher refreshes itself from the agent and
      re-applies its privilege. Live sessions ride the roll out - they reconnect onto the new agent
      by themselves.
    </p>
    <p>
      Moving itself means the deployment's <strong>tag is rewritten</strong> when it pins a release
 - <code>softwarity/plug:2.3.0</code> becomes <code>softwarity/plug:2.4.0</code>, majors
      included. plug is infrastructure carrying your sessions, not an application dependency you
      hold back for reproducibility; and re-resolving a pinned tag could only ever return the same
      image, which made <code>update</code> a no-op exactly where it was needed. Each backend
      applies it its own way: Swarm updates the service's image, Kubernetes patches the
      Deployment's container image, and a plain container - which cannot recreate itself - pulls
      the new image and hands you the one command that does.
    </p>
    <p>
    <p>
      <code>plug update</code> follows the tag the deployment already carries. To move a cluster to a
      <strong>different</strong> one, name it: <code>plug -p neo update tag</code> takes the newest
      release published, <code>plug -p neo update latest</code> the latest stream, and
      <code>plug -p neo update feat-09</code> a branch's tag at whatever it points to now - an exact
      release (<code>2.3.0</code>) works too, downgrades included. The agent checks the tag exists on
      the registry before repointing anything: aiming a deployment at a tag nobody published would
      leave you with an agent that cannot pull.
    </p>
    <p>
      A <strong>moving</strong> tag (<code>latest</code>, <code>main</code>, a branch) is left
      exactly as it is and simply re-pulled: it already resolves to whatever its publisher last
      pushed, and repointing it would override a deliberate choice. And when a pinned deployment is
      already on the newest release, plug says so immediately instead of rolling the workload to
      find out.
    </p>

    <h3>Where to next</h3>
    <p>
      Read the <a routerLink="/cli">CLI reference</a> for every command,
      <a routerLink="/how-it-works">How it works</a> for the mechanics,
      <a routerLink="/profiles">Profiles &amp; versions</a> for day-to-day usage,
      <a routerLink="/swarm">Swarm</a> for the cluster side,
      <a routerLink="/security">Security model</a> before deploying anywhere sensitive, the
      <a routerLink="/coverage">Coverage matrix</a> for what works on which OS, and the
      <a routerLink="/roadmap">Roadmap</a> for what's coming.
    </p>
  `,
})
export class GettingStartedComponent {}
