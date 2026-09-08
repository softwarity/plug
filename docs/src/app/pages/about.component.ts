import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { MatIconModule } from '@angular/material/icon';

@Component({
  selector: 'app-about',
  imports: [CodeComponent, RouterLink, MatIconModule],
  styles: [
    `
      .diagram {
        margin: 22px 0 6px;
        overflow-x: auto;
      }
      .diagram picture {
        display: block;
      }
      .diagram img {
        display: block;
        width: 100%;
        min-width: 640px;
        height: auto;
      }
      .cap {
        color: var(--text-muted);
        font-size: 0.82rem;
        margin: 4px 2px 26px;
      }

      .dirs {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
        gap: 14px;
        margin: 0 0 28px;
      }
      .dir {
        padding: 14px 16px;
        background-color: var(--bg-secondary);
        border: 1px solid var(--border-color);
        border-radius: 8px;
        font-size: 0.9rem;
        line-height: 1.5;
        color: var(--text-secondary);
      }
      .dir strong {
        color: var(--text-primary);
      }
      .dir-tag {
        display: inline-block;
        font-size: 0.7rem;
        font-weight: 700;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        padding: 2px 8px;
        border-radius: 999px;
        margin-bottom: 8px;
      }
      .dir-tag.out {
        color: #a371f7;
        background: rgba(163, 113, 247, 0.14);
      }
      .dir-tag.in {
        color: var(--accent-green);
        background: rgba(63, 185, 80, 0.14);
      }

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
      .cta {
        margin: 4px 0 8px;
      }
      .support {
        color: var(--text-secondary);
        font-size: 0.88rem;
        line-height: 1.55;
        background: var(--bg-secondary);
        border: 1px solid var(--border-color);
        border-radius: 8px;
        padding: 12px 15px;
        margin: 22px 0 8px;
      }
      .support strong {
        color: var(--text-primary);
      }
      .cta a {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        padding: 10px 18px;
        border-radius: 8px;
        background: var(--accent-purple);
        color: #0d1117;
        font-weight: 600;
        text-decoration: none;
      }
      .cta a:hover {
        text-decoration: none;
        filter: brightness(1.08);
      }

    `,
  ],
  template: `
    <h2>What plug does</h2>

    <p>
      <strong>plug</strong> runs a process on your machine as a
      <strong>full member of your cluster</strong> - in both directions. Your process reaches
      cluster services by their real names, and it is itself reachable from inside the cluster under
      a name. No code change, no proxy configuration - you just prefix your usual command:
    </p>

    <app-code lang="text">plug -s my-app:8080:3000 npm run start:dev</app-code>

    <div class="diagram">
      <!--
        The SVG animates 18 SMIL tracks on a 16s loop, forever. It is an <img>,
        so neither this component's CSS nor a <style> inside the file reaches
        it, and SMIL has no CSS off switch anyway: prefers-reduced-motion had
        no way to be honoured here.

        <picture> is the way in. The deploy pipeline already renders a still
        frame of the same diagram (deploy-doc.yml: .frames/f180.png ->
        media/about-diagram-hero.png, served at /plug/media/), so a reader who
        asked for less motion gets that one image and never downloads the
        animated file. width/height are the SVG's own 900x511, so the caption
        and everything below stop jumping while it loads.
      -->
      <picture>
        <source media="(prefers-reduced-motion: reduce)" srcset="media/about-diagram-hero.png" />
        <img src="assets/about-diagram.svg" width="900" height="511" (error)="stillMissing($event)" alt="plug in two animated rounds: the browser's GET data is served by the deployed service1, then plug parks it and the same request is served by your machine - which also queries postgres by name." />
      </picture>
    </div>
    <p class="cap">
      One SSH tunnel carries both directions: your process reaches the cluster by name, and the
      cluster reaches your process by the name you serve with <code>-s</code>. Already deployed?
      plug <strong>parks</strong> the in-cluster <code>service1</code> for the session - your
      process takes its place, and it is restored when the session ends.
    </p>

    <div class="dirs">
      <div class="dir">
        <span class="dir-tag out">outbound</span><br />
        <strong>Reach the cluster by name.</strong> Your process addresses
        <code>postgres</code>, <code>my-service:8080</code> - the same names any workload inside
        uses. No port-forwards, no <code>localhost:PORT</code> mappings. Only consuming (a DB
        tool, a one-off script)? That's <code>plug -c</code>.
      </div>
      <div class="dir">
        <span class="dir-tag in">inbound</span><br />
        <strong>Be reachable by name.</strong> With <code>-s</code>, a name you serve is reachable
        from inside the cluster - a gateway, another service, or a browser through the ingress lands
        on your machine. No name pre-declared, no redeploy.
      </div>
    </div>

    <div class="callout">
      <strong>Two pieces.</strong> A tiny <a routerLink="/swarm">agent container</a> (Alpine, one
      static Go binary) deployed once on the cluster - and a single static <code>plug</code> binary on each dev
      machine. Set up once per cluster; after that, day-to-day runs need no sudo or admin.
    </div>

    <h3>What you get</h3>
    <section class="features">
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">dns</mat-icon>
        <span class="feature-title">Names, resolved cluster-side</span>
        <span class="feature-desc">Address <code>my-service:8080</code> by its real name - no <code>localhost:PORT</code> mappings, no <code>/etc/hosts</code> edits.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">all_inclusive</mat-icon>
        <span class="feature-title">Every runtime, unchanged</span>
        <span class="feature-desc">Traffic is captured at the IP layer, so your app's socket is never touched - Node, the JVM, Python, <strong>Go</strong>, curl, gRPC, DB drivers all just work.</span>
      </a>
      <a routerLink="/how-it-works" class="feature-card">
        <mat-icon class="feature-icon">swap_horiz</mat-icon>
        <span class="feature-title">Reachable from the cluster</span>
        <span class="feature-desc"><code>-s</code> publishes a local port under a cluster name - a gateway or workload reaches your process, for the session. A deployed service owning the name is <strong>parked</strong> meanwhile, restored on exit.</span>
      </a>
      <a routerLink="/profiles" class="feature-card">
        <mat-icon class="feature-icon">hub</mat-icon>
        <span class="feature-title">Several clusters at once</span>
        <span class="feature-desc">Run the same process against two clusters in parallel - each session stays isolated.</span>
      </a>
      <a routerLink="/swarm" class="feature-card">
        <mat-icon class="feature-icon">devices</mat-icon>
        <span class="feature-title">Linux · macOS · Windows</span>
        <span class="feature-desc">Native on all three (no WSL2 needed); a multi-arch <code>amd64</code>/<code>arm64</code> agent image.</span>
      </a>
      <a routerLink="/security" class="feature-card">
        <mat-icon class="feature-icon">shield</mat-icon>
        <span class="feature-title">Honest security model</span>
        <span class="feature-desc">Deliberately auth-less, for trusted dev clusters - read the model before deploying.</span>
      </a>
    </section>

    <h3>How plug compares</h3>
    <p>
      plug isn't the only way to run local code against a remote cluster:
      <a href="https://metalbear.com/mirrord/" target="_blank" rel="noopener">mirrord</a> and
      <a href="https://telepresence.io/" target="_blank" rel="noopener">Telepresence</a> are the
      well-known Kubernetes-native tools. plug's angle is that it works the same on Docker,
      Compose, Swarm <em>and</em> Kubernetes, from Linux, macOS and Windows.
      <a routerLink="/comparison">Side by side, including where they are ahead</a>.
    </p>

    <p class="cta">
      <a routerLink="/getting-started">Set it up <mat-icon aria-hidden="true" style="font-size:18px;width:18px;height:18px">arrow_forward</mat-icon></a>
    </p>

    <p class="support">
      <strong>plug is free, and there is no paid tier planned.</strong> If it saves you the afternoon
      it was built to save, you can
      <a href="https://github.com/sponsors/softwarity" target="_blank" rel="noopener">sponsor its
      development</a> - one-off or monthly, whatever it is worth to you. Nothing in plug is gated
      behind it, and nothing ever will be. Companies whose use falls outside the
      <a href="https://github.com/softwarity/plug#license" target="_blank" rel="noopener">licence</a>
      should ask about that instead.
    </p>
  `,
})
export class AboutComponent {
  // The still frame is a DEPLOY artifact: deploy-doc.yml renders it after the
  // Angular build, so it exists on Pages and nowhere else - not in `ng serve`,
  // not in a local `npm run build`. A <picture> that picked a missing <source>
  // shows a broken image and does NOT fall back on its own. Drop the source and
  // reload the <img>, which then resolves to the animated SVG. It runs at most
  // once (no source left to remove afterwards), and it also covers the day the
  // render step fails in CI.
  protected stillMissing(e: Event): void {
    const img = e.target as HTMLImageElement;
    const source = img.parentElement?.querySelector('source');
    if (!source) return;
    source.remove();
    img.src = 'assets/about-diagram.svg';
  }
}
