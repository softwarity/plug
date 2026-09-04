import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-profiles',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Profiles &amp; versions</h2>

    <p>
      A profile is one cluster: a plain file in <code>~/.plug/&lt;name&gt;.conf</code>. plug picks
      the profile automatically, so your daily command carries no <code>-p</code>.
    </p>

    <h3>Automatic selection</h3>
    <p>
      The <a routerLink="/getting-started">installer</a> normally creates a profile for you, named
      after the cluster host it reads from your <code>ssh get@&lt;host&gt;</code> command - so the
      wizard below is really just the fallback when no profile exists yet. Install from several
      clusters and each gets its own profile.
    </p>
    <ul>
      <li><strong>No profile</strong> → a short wizard asks for a name, the cluster host and the agent port (default <code>2222</code>), saves the profile and uses it immediately.</li>
      <li><strong>One profile</strong> → used as is (plug prints which one, for transparency).</li>
      <li><strong>Several profiles</strong> → interactive numbered choice - or skip the question with <code>-p &lt;name&gt;</code>.</li>
    </ul>

    <app-code lang="text">$ plug -s my-app:8080:3000 npm run start:dev
[plug] no profile in /Users/you/.plug - let's create one
profile name [default]:
cluster host: swarm-node.example.com
agent port [2222]:
[plug] profile "default" saved to /Users/you/.plug/default.conf
[plug] running your command</app-code>

    <h3>The profile file</h3>
    <p>Created by the wizard, editable by hand - just a host and a port:</p>
    <app-code lang="text"># ~/.plug/staging.conf
host = swarm-node.example.com
port = 2222</app-code>

    <h3>What goes to the cluster vs direct (split-horizon)</h3>
    <p>
      plug decides per destination by the <strong>shape of the name</strong>:
    </p>
    <ul>
      <li><strong>Single-label names</strong> (<code>pdfbox</code>, <code>mongodb</code>) → the cluster, resolved cluster-side.</li>
      <li><strong>Dotted FQDNs</strong> (<code>api.github.com</code>, a LAN host) → resolved and connected <strong>directly</strong> on your machine.</li>
      <li><code>localhost</code> and <code>127.0.0.1</code> always stay local.</li>
    </ul>

    <h3>Several clusters at once</h3>
    <p>
      Run the same process against two clusters in parallel - each stays isolated:
    </p>
    <app-code lang="bash">plug -p prod -s my-app:8080:3000 npm run start   # → cluster prod
plug -p staging -s my-app:8080:3000 npm run start   # → cluster staging, side by side</app-code>
    <p>How plug keeps parallel clusters apart differs by OS:</p>
    <ul>
      <li>
        <strong>Linux</strong> - each launch runs in its own <strong>mount namespace</strong> with a
        private resolver, so two launches never share DNS: isolation for free.
      </li>
      <li>
        <strong>Windows</strong> - the SYSTEM service holds <strong>one tunnel per cluster</strong>
        and attributes each connection to the right one <strong>at <code>connect()</code></strong>,
        walking the process back to the <code>plug -p</code> that launched it (PID-at-connect).
      </li>
      <li>
        <strong>macOS</strong> - the <strong>same PID-at-connect design</strong> as Windows: the global
        daemon holds one tunnel per cluster and routes each flow to the right one at
        <code>connect()</code> (proven simultaneously in CI).
      </li>
    </ul>
    <p>
      See <a routerLink="/how-it-works">how plug tells them apart</a> and the
      <a routerLink="/coverage">coverage matrix</a>.
    </p>

    <h3>Managing profiles</h3>
    <app-code lang="bash">plug ls                  # list profiles: name  host:port
plug test [profile]      # check an agent is reachable (prints its version)
plug rn default prod     # rename a profile (alias: plug mv)
plug rm staging          # remove a profile</app-code>
    <p>
      <code>plug test</code> connects to the agent and reports its version - handy to confirm a
      cluster is reachable before running your app.
    </p>

    <h3>One-shot, without a profile</h3>
    <p>
      Point plug straight at a cluster with <code>--host</code> (and <code>--port</code> if it isn't
      <code>2222</code>) - handy for a quick try or for CI. It overrides the selected profile for
      that one run:
    </p>
    <app-code lang="bash">plug --host swarm-node.example.com --port 2222 -s my-app:8080:3000 npm run start:dev</app-code>

    <h3>Versions - the launcher model</h3>
    <p>
      plug is a small <strong>launcher</strong>, like <code>nvm</code> or <code>rustup</code>. The
      binary on your <code>PATH</code> does almost nothing itself: on each run it asks the agent
      which version it speaks, then executes <em>that exact version</em> from
      <code>~/.plug/versions/</code> - downloading it once from the cluster if missing.
    </p>
    <ul>
      <li><strong>Each cluster runs its own version.</strong> A cluster on 1.1 and one on 1.3 both work - no in-place replacement, no upgrade churn on the binary you installed.</li>
      <li><strong>Correct by construction.</strong> The CLI can never drift from the agent it talks to, because it literally runs the agent's version.</li>
      <li><strong>Cheap.</strong> Cached binaries are a few MB each; the first connect to a new version pays one small download.</li>
    </ul>
    <app-code lang="bash">plug version         # the launcher's own version
plug versions        # launcher + every cached cluster version</app-code>
    <p>
      The launcher itself almost never needs updating: the download protocol it speaks to the agent
      (<code>version</code> + <code>&lt;os-arch&gt;</code>) is frozen - if it ever must change, just
      reinstall it from the cluster.
    </p>

    <h3>Updating a cluster</h3>
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
  `,
})
export class ProfilesComponent {}
