import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-cli',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>CLI reference</h2>

    <p>
      Every plug command on one page, each linking to the section that details it.
    </p>

    <h3>Running a command</h3>
    <p>
      One invocation shape, two stances — a process either <strong>serves a name</strong> in the
      cluster or is a <strong>pure client</strong> of it (one or the other, never neither):
    </p>
    <app-code lang="bash">plug [-p profile] -s my-app:8080:3000 npm run start:dev   # a service: the cluster calls it by name
plug [-p profile] -c psql -h postgres                     # a pure client: DB tools, one-off scripts</app-code>
    <p>
      Details in <a routerLink="/getting-started">Getting started</a>; mechanics in
      <a routerLink="/how-it-works">How it works</a>.
    </p>

    <h3>Commands</h3>
    <table>
      <tr>
        <th>command</th>
        <th>what it does</th>
        <th>details</th>
      </tr>
      <tr>
        <td><code>plug ls</code></td>
        <td>list profiles (name, host:port)</td>
        <td><a routerLink="/profiles">Profiles</a></td>
      </tr>
      <tr>
        <td><code>plug test [profile]</code></td>
        <td>check an agent is reachable, print its version</td>
        <td><a routerLink="/profiles">Profiles</a></td>
      </tr>
      <tr>
        <td><code>plug doctor [-p profile] [--fix]</code></td>
        <td>
          health-check everything plug touches, remedy next to each finding; <code>--fix</code>
          applies the safe repairs; offers a pre-filled (redacted) GitHub issue
        </td>
        <td><a routerLink="/getting-started">Getting started</a></td>
      </tr>
      <tr>
        <td><code>plug update [-p profile]</code></td>
        <td>the agent refreshes itself from its registry, then this launcher from the agent</td>
        <td><a routerLink="/getting-started">Getting started</a></td>
      </tr>
      <tr>
        <td><code>plug rn &lt;old&gt; &lt;new&gt;</code></td>
        <td>rename a profile (alias: <code>mv</code>)</td>
        <td><a routerLink="/profiles">Profiles</a></td>
      </tr>
      <tr>
        <td><code>plug rm &lt;profile&gt;</code></td>
        <td>remove a profile</td>
        <td><a routerLink="/profiles">Profiles</a></td>
      </tr>
      <tr>
        <td><code>plug version [-p profile]</code></td>
        <td>
          bare: this launcher's version; with a profile (or <code>-H</code>): that cluster
          <em>agent's</em> version — one bare value, script-friendly
        </td>
        <td><a routerLink="/profiles">Versions</a></td>
      </tr>
      <tr>
        <td><code>plug versions</code></td>
        <td>the whole picture: launcher, cached cores, and each profile's agent version</td>
        <td><a routerLink="/profiles">Versions</a></td>
      </tr>
      <tr>
        <td><code>plug about</code></td>
        <td>what plug is, in a few lines</td>
        <td><a routerLink="/">About</a></td>
      </tr>
      <tr>
        <td><code>plug uninstall</code></td>
        <td>remove plug from this machine (binary, cache, profiles — it lists, you confirm)</td>
        <td></td>
      </tr>
    </table>

    <h3>Options</h3>
    <table>
      <tr>
        <th>option</th>
        <th>what it does</th>
        <th>details</th>
      </tr>
      <tr>
        <td><code>-p, --profile &lt;name&gt;</code></td>
        <td>use profile <code>~/.plug/&lt;name&gt;.conf</code> (created on first use)</td>
        <td><a routerLink="/profiles">Profiles</a></td>
      </tr>
      <tr>
        <td><code>-H, --host &lt;host&gt;</code> <code>--port &lt;port&gt;</code></td>
        <td>point at a cluster directly, no profile (default port <code>2222</code>)</td>
        <td><a routerLink="/profiles">Profiles</a></td>
      </tr>
      <tr>
        <td><code>-s, --serve &lt;name:cluster-port:local-port&gt;</code></td>
        <td>
          serve this process under a cluster name for the session (repeatable; a deployed owner of
          the name is parked, then restored). <code>local-port</code> may be a
          <strong>name</strong> instead of a number — plug then picks a free port per session and
          passes it to your command as <code>&#123;NAME&#125;</code> and <code>$NAME</code>
        </td>
        <td><a routerLink="/getting-started">Getting started</a></td>
      </tr>
      <tr>
        <td><code>-c, --client</code></td>
        <td>pure consumer — nothing named, no port reserved on the agent</td>
        <td><a routerLink="/getting-started">Getting started</a></td>
      </tr>
      <tr>
        <td><code>-h, --help</code></td>
        <td>show the built-in help (this page, condensed)</td>
        <td></td>
      </tr>
    </table>

    <h3>Rarely needed</h3>
    <ul>
      <li><code>plug down</code> — tear down plug's background state now (it does so by itself ~30&nbsp;s after the last session; this is the impatient path, and the remedy <a routerLink="/getting-started">doctor</a> points at for a stale resolver).</li>
      <li><code>plug install-service</code> / <code>plug remove-service</code> — Windows only: create/remove the datapath service (one elevated run; the <a routerLink="/getting-started">installer</a> already does it).</li>
      <li><code>plug init</code> — run the profile wizard explicitly (it runs by itself when no profile exists).</li>
    </ul>
  `,
})
export class CliComponent {}
