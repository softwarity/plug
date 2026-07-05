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
    <p>
      The <a routerLink="/getting-started">installer</a> normally creates a profile for you, named
      after the cluster host it reads from your <code>ssh get@&lt;host&gt;</code> command — so the
      wizard below is really just the fallback when no profile exists yet. Install from several
      clusters and each gets its own profile.
    </p>
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
[plug] socks5 proxy on 127.0.0.1:54123
[plug] tunnel ready — running your command</app-code>

    <h3>The profile file</h3>
    <p>Created by the wizard, editable by hand:</p>
    <app-code lang="text"># ~/.plug/staging.conf
host = swarm-node.example.com
port = 2222
# ONLY for a Go/statically-linked binary the hook can't intercept, that reads
# its target from the env (a libc app — Node, JVM, Python — needs nothing):
forward = DATABASE_URL=postgres://odb:5432/appdb</app-code>

    <h3>Port-forwards — the escape hatch</h3>
    <p>
      On macOS and Linux the injected <a routerLink="/how-it-works">connect()/DNS hook</a> already
      routes any <strong>libc</strong> process's TCP by name (Node, the JVM, Python…), so
      <code>amqplib</code>/<code>pg</code>/<code>redis</code> need nothing. Port-forwards are the
      fallback for what the hook can't reach — <strong>Go</strong>/statically-linked binaries (they
      bypass libc), non-TCP, or when injection is disabled. Unlike the transparent hook, a forward
      <strong>rewrites an env var</strong> (it does not intercept): declare
      <code>forward = ENV=url</code>, and plug opens a per-session local port to the cluster service
      and sets <code>ENV</code> to that local address (scheme and credentials preserved). It only
      helps an app that reads its target from <code>ENV</code> — a hardcoded address won't use it.
    </p>
    <app-code lang="text">forward = DATABASE_URL=postgres://user:pass@odb:5432/appdb
# the Go child sees:  DATABASE_URL=postgres://user:pass@127.0.0.1:54210/appdb</app-code>
    <p>
      Your 12-factor app reads its connection string from the env, so no code changes. Each session
      gets its own ports, so several clusters never collide.
    </p>

    <h3>What goes to the cluster vs direct (split-horizon)</h3>
    <p>
      plug decides per destination by the <strong>shape of the name</strong> (restoring the original
      split-horizon policy), and falls back to the other side if the first fails:
    </p>
    <ul>
      <li><strong>Single-label names</strong> (<code>pdfbox</code>, <code>mongodb</code>) → the cluster, resolved cluster-side.</li>
      <li><strong>Dotted FQDNs</strong> (<code>api.github.com</code>, a LAN host) → resolved and connected <strong>directly</strong> on your machine — no longer tunnelled through the cluster.</li>
      <li><code>localhost</code> and <code>127.0.0.1</code> always stay local.</li>
    </ul>
    <p>
      Force extras direct with <code>PLUG_DIRECT</code> — a comma-separated list of CIDRs, IPs,
      hostnames or suffixes that must bypass the cluster (e.g. reach a service on your LAN):
    </p>
    <app-code lang="bash">PLUG_DIRECT=192.168.0.0/16,.corp.example.com plug npm run start:dev</app-code>

    <h3>Multiple clusters at once</h3>
    <p>
      There is no global state (no system DNS, no <code>/etc/hosts</code>, no firewall, no TUN), so
      the same process can run against several clusters in parallel — each session has its own proxy
      and forward ports:
    </p>
    <app-code lang="bash">plug -p prod    npm run start   # → cluster prod
plug -p staging npm run start   # → cluster staging, side by side</app-code>

    <h3>Managing profiles</h3>
    <app-code lang="bash">plug ls                  # list profiles: name  host:port
plug test [profile]      # check an agent is reachable (prints its version)
plug rn default prod     # rename a profile (alias: plug mv)
plug rm staging          # remove a profile</app-code>
    <p>
      <code>plug test</code> connects to the agent and reports its version — handy to confirm a
      cluster is reachable before running your app.
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
