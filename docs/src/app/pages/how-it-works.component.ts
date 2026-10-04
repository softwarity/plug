import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';

@Component({
  selector: 'app-how-it-works',
  imports: [RouterLink],
  styles: [
    `
      .diagram {
        margin: 20px 0 8px;
        padding: 8px 4px;
        overflow-x: auto;
      }
      .diagram img {
        display: block;
        width: 100%;
        min-width: 620px;
        max-width: 900px;
        height: auto;
        margin: 0 auto;
      }
      .diagram + .cap {
        color: var(--text-secondary);
        font-size: 0.92rem;
      }
    `,
  ],
  template: `
    <h2>How it works</h2>

    <p>
      <strong>Your process runs on your machine and behaves as if it ran in the cluster.</strong>
      It calls the cluster's services by their names, the cluster calls it by its name, and it has
      the environment, the files and the data of the service it stands in for. Nothing changes in
      your code or its configuration: you start it with <code>plug</code> in front.
    </p>

    <!--
      The same animated diagram as the About page. An <img>, not an inline SVG:
      the animation is SMIL, which has no CSS off switch, so <picture> is what
      honours prefers-reduced-motion. The still frame is rendered by the deploy
      pipeline and exists on Pages only; where it is missing the source is
      dropped and the <img> shows the animated file (see stillMissing).
    -->
    <div class="diagram">
      <picture>
        <source media="(prefers-reduced-motion: reduce)" srcset="media/about-diagram-hero.png" />
        <img src="assets/about-diagram.svg" width="900" height="511" (error)="stillMissing($event)" alt="plug in two animated rounds: the browser's GET data is served by the deployed service1, then plug parks it and the same request is served by your machine, which also queries postgres by name." />
      </picture>
    </div>

    <h3>One connection, both directions</h3>
    <p>
      plug keeps a single encrypted connection between your machine and a small
      <strong>agent</strong> that lives in the cluster. Everything travels through it.
    </p>
    <ul>
      <li>
        <strong>Outbound: your process reaches the cluster.</strong> When it asks for
        <code>postgres</code> or <code>rabbitmq</code>, the name is resolved in the cluster and the
        connection is opened from inside it, by the agent. Names that are not the cluster's, a public
        API or <code>localhost</code>, go out as they always did.
      </li>
      <li>
        <strong>Inbound: the cluster reaches your process.</strong> With
        <code>-s api:80:3000</code> your process takes the name <code>api</code> in the cluster, and
        whatever calls <code>api</code> there lands on your local port.
      </li>
    </ul>

    <h3>Taking a deployed service's place</h3>
    <p>
      If the name you serve already belongs to a deployed service, plug <strong>parks</strong> it
      for the session: it stops answering, and your process answers instead. When your command ends,
      the service is <strong>restored</strong> as it was. Nobody redeploys anything.
    </p>
    <p>Standing in for a service means being it, so your process receives what the service had:</p>
    <ul>
      <li>its <strong>environment</strong>, secrets included, with the cluster's values;</li>
      <li>its <strong>secret and config files</strong>, a certificate or a keystore;</li>
      <li>its <strong>data</strong>: the volumes it mounts, live.</li>
    </ul>
    <p>
      Each of the three can be declined or borrowed from another service: see
      <code>--no-env</code>, <code>--env-of</code> and <code>--no-mount</code> in the
      <a routerLink="/cli">CLI reference</a>.
    </p>

    <h3>Its volumes, live</h3>
    <div class="diagram">
      <picture>
        <source media="(prefers-reduced-motion: reduce)" srcset="assets/mount-diagram-still.png" />
        <img src="assets/mount-diagram.svg" width="900" height="511" loading="lazy" alt="A workload's volume in two animated rounds: the deployed api mounts its PVC or volume and reads and writes it; then plug parks api, a plug helper mounts the same claim, and your local api writes a file that travels through the SSH tunnel and the agent to the helper, which writes it on the real volume." />
      </picture>
    </div>
    <p class="cap">
      The claim on the storage, a PVC or a volume, stays where it is. While your session lives,
      a <strong>plug helper</strong> started by the agent mounts it in place of the parked
      workload, and what your process reads and writes goes through the tunnel and the agent to
      that helper: its files are the real volume, <strong>nothing is copied</strong>. The helper
      is removed when the session ends, and the workload gets its claim back.
    </p>

    <h3>What it asks of your machine</h3>
    <p>
      Nothing in your application: no proxy setting, no library, no rewritten URL, whatever the
      language or the runtime. plug itself needs a system permission to route the cluster's names,
      granted <strong>once, at install</strong>; after that <code>plug &lt;cmd&gt;</code> runs as
      you. In the cluster, the agent is the only thing deployed.
    </p>

    <h3>A session cleans up after itself</h3>
    <p>
      Everything plug sets up belongs to the session: the name it serves, the service it parked,
      the helper on a volume. They go when your command ends. If your machine disappears instead,
      the agent notices and puts things back within the minute. And a connection that drops, a
      laptop that sleeps, a VPN that reconnects, an agent that restarts, is re-established without
      restarting your command.
    </p>

    <h3>Several clusters at once</h3>
    <p>
      Two terminals can be plugged into two different clusters at the same time,
      <code>plug -p a</code> and <code>plug -p b</code>, each resolving the same service names to
      its own cluster. See <a routerLink="/profiles">Profiles</a>.
    </p>

    <h3>For an AI coding agent</h3>
    <p>
      <code>plug mcp</code> serves what plug knows to an agent, as tools with structured answers
      rather than prose for a person: which clusters this machine knows, what <code>doctor</code>
      finds and the exact remedy, the environment a deployed workload runs with, whether a name
      exists. An agent that reads "the pod's variables came through empty because the role lacks
      <code>pods/exec</code>" can fix that; one that reads a wall of text guesses. It is the
      launcher itself, over stdio, so nothing listens on a port and every cluster's version is
      handled the way it already is.
    </p>

    <h3>Going further</h3>
    <p>
      The mechanics, how names are captured, what the permission is on each OS, how flows are kept
      apart, are in <a routerLink="/under-the-hood">Under the hood</a>. What the agent may do in
      your cluster, and who may reach it, is in the <a routerLink="/security">Security model</a>.
    </p>
  `,
})
export class HowItWorksComponent {
  // The still frame of the About diagram is rendered by the deploy pipeline and
  // exists on Pages only. A <picture> that picked a missing <source> shows a
  // broken image and does not fall back on its own: drop the source and reload
  // the <img>, which then resolves to the animated SVG. Runs at most once.
  protected stillMissing(e: Event): void {
    const img = e.target as HTMLImageElement;
    const source = img.parentElement?.querySelector('source');
    if (!source) return;
    source.remove();
    img.src = 'assets/about-diagram.svg';
  }
}
