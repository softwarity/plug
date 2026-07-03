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
  `,
})
export class ProfilesComponent {}
