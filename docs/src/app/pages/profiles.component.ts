import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-profiles',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Profiles &amp; wizard</h2>

    <p>
      A profile is one cluster: a plain file in <code>~/.plug/&lt;name&gt;.conf</code>. plug picks
      the profile automatically so the daily command stays just <code>plug &lt;your command&gt;</code>.
    </p>

    <h3>Automatic selection</h3>
    <ul>
      <li><strong>No profile</strong> → a short wizard asks for a name, the cluster host and the agent port (default <code>2222</code>), saves the profile and uses it immediately.</li>
      <li><strong>One profile</strong> → used as is (plug prints which one, for transparency).</li>
      <li><strong>Several profiles</strong> → interactive numbered choice — or skip the question with <code>-p &lt;name&gt;</code>.</li>
    </ul>

    <app-code lang="text">$ plug npm run start:dev
[plug] no profile in /Users/you/.plug — let's create one
profile name [default]:
cluster host: swarm-node.example.com
agent port [2222]:
[plug] profile "default" saved to /Users/you/.plug/default.conf
[plug] routing 10.0.9.0/24 via swarm-node.example.com:2222
[plug] tunnel up — cluster DNS and subnets are now reachable</app-code>

    <h3>The profile file</h3>
    <p>Created by the wizard, editable by hand:</p>
    <app-code lang="text"># ~/.plug/staging.conf
host = swarm-node.example.com
port = 2222
# subnets = 10.0.9.0/24,10.0.10.0/24   (optional, skips auto-discovery)</app-code>
    <p>
      <code>subnets</code> is the only optional key: set it to pin the routed networks instead of
      <a routerLink="/how-it-works">auto-discovery</a> — useful for exotic topologies, or for
      Kubernetes today (<a routerLink="/roadmap">Roadmap</a>).
    </p>

    <h3>plug init</h3>
    <p>
      Runs the same wizard on demand — typically to add a second cluster. It asks before
      overwriting an existing profile:
    </p>
    <app-code lang="bash">plug init                      # create/update a profile interactively
plug -p staging npm run start:dev</app-code>

    <h3>Bypassing profiles</h3>
    <p>Flags and environment variables skip the profile logic entirely — handy for one-shots and CI:</p>
    <app-code lang="bash">plug --host swarm-node.example.com --port 2222 npm run start:dev
PLUG_HOST=swarm-node.example.com plug ./mvnw spring-boot:run</app-code>

    <div class="callout">
      Precedence, highest first: <code>--host</code>/<code>--port</code> flags →
      <code>$PLUG_HOST</code>/<code>$PLUG_PORT</code> → the selected profile.
    </div>

    <h3>Versions — the launcher model</h3>
    <p>
      plug is a small <strong>launcher</strong>, like <code>nvm</code> or <code>rustup</code>. The
      binary on your <code>PATH</code> does almost nothing itself: on each run it asks the agent
      which version it speaks, then executes <em>that exact version</em> from
      <code>~/.plug/versions/</code> — downloading it once from the cluster if missing.
    </p>
    <ul>
      <li><strong>Each cluster runs its own version.</strong> A cluster on 0.2 and one on 0.5 both work — no in-place replacement, no downgrade/upgrade churn on the binary you installed.</li>
      <li><strong>Correct by construction.</strong> The CLI can never drift from the agent it talks to, because it literally runs the agent's version.</li>
      <li><strong>Cheap.</strong> Cached binaries are a couple of MB each; the first connect to a new version pays one small download.</li>
    </ul>
    <app-code lang="bash">plug version         # the launcher's own version
plug versions        # launcher + every cached cluster version
plug self-update     # update the launcher itself (rare — only if bootstrap changes)</app-code>
    <p>
      The launcher itself almost never needs updating: the download protocol it speaks to the agent
      (<code>version</code> + <code>&lt;os-arch&gt;</code>) is frozen. If it ever must change,
      <code>plug self-update</code> replaces the launcher from any cluster.
    </p>
  `,
})
export class ProfilesComponent {}
