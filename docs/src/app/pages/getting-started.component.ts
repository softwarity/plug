import { NgTemplateOutlet } from '@angular/common';
import { Component, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { FileComponent } from '../file/file.component';

type Os = 'linux' | 'macos' | 'windows';

/** Guessed from the browser, because the first thing this page asks is the one
 *  thing it can already tell. Wrapped: no window during a build, and a browser
 *  that reports nothing useful just lands on Linux. */
function guessOs(): Os {
  const ua = typeof navigator === 'undefined' ? '' : navigator.userAgent;
  if (/Windows/i.test(ua)) return 'windows';
  if (/Mac OS X|Macintosh/i.test(ua)) return 'macos';
  return 'linux';
}

@Component({
  selector: 'app-getting-started',
  imports: [CodeComponent, FileComponent, RouterLink, NgTemplateOutlet],
  template: `
    <!-- One switch, written once and placed wherever the reading depends on it.
         Repeating it is the point: the top of the page is out of sight by
         section 2, and a reader who cannot see which system a command is for
         does not know there is a choice to make. The active button names the
         system, so the same control answers "which one is this?" and "give me
         the other one" without a trip back up. -->
    <ng-template #osPicker>
      <div class="os" role="group" aria-label="Choose your operating system">
        @for (o of oses; track o.id) {
          <button
            type="button"
            [class.on]="os() === o.id"
            [attr.aria-pressed]="os() === o.id"
            (click)="os.set(o.id)"
          >
            {{ o.label }}
          </button>
        }
      </div>
    </ng-template>

    <div class="head">
      <h2>Getting started</h2>
      <ng-container *ngTemplateOutlet="osPicker" />
    </div>

    <p>
      Two pieces: a small <a routerLink="/swarm">agent</a> on the cluster, and the
      <code>plug</code> CLI on each dev machine. Set up once per cluster - after that, day-to-day
      runs need no sudo or admin. New here? See <a routerLink="/">what plug does</a> first.
    </p>

    <h3>1. Deploy the agent, once, on the cluster</h3>
    <p>
      Add the service to the stack you want to reach - it joins the stack's network automatically:
    </p>
    <app-file
      src="assets/plug-service.yml"
      download="plug-service.yml"
      [initial]="'opened'"
      [preview]="16"
    />
    <p>
      The socket line is <strong>required</strong> on Docker, Compose and Swarm: it is how the agent
      creates your <a routerLink="/swarm"><code>-s</code> name</a>, and an agent without it
      <strong>refuses to start</strong> rather than fail on your first <code>-s</code>. It is root on
      the host, so mount it only on a cluster you trust. Kubernetes needs no socket - a Services-only
      RBAC role instead, and the same rule applies. See
      <a routerLink="/swarm">Swarm</a> for the standalone variant, or
      <a routerLink="/kubernetes">Kubernetes</a> for the cluster.
    </p>

    <h3>2. Install the CLI, on each dev machine</h3>
    <p>
      One line, straight from the cluster - the agent hands over the right binary. The install
      grants plug its privilege <strong>once</strong>, so that no later run ever needs it.
    </p>
    <p class="for-os">Commands below are for <ng-container *ngTemplateOutlet="osPicker" /></p>
    @if (os() === 'windows') {
      <p>
        From Git Bash, the assumed Windows shell - it ships with
        <a href="https://git-scm.com/download/win" target="_blank" rel="noopener">Git for Windows</a
        >. The elevated step is the datapath service, created once:
      </p>
      <app-code lang="bash">cluster=&lt;cluster-host&gt;
ssh -n -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get&#64;$cluster install-windows \\
  | bash -s -- $cluster 2222</app-code>
    } @else {
      <p>A single <code>sudo</code>, inside the installer:</p>
      <app-code lang="bash"># the agent regenerates its host key each start (not a secret here) - skip the check
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get&#64;&lt;cluster-host&gt; install | sh</app-code>
    }
    <p>
      The installer reads the cluster address straight from <em>your</em> <code>ssh</code> command
      and saves a <a routerLink="/profiles">profile named after that host</a>, so plug is ready right
      away. Install from a second cluster and you get a second profile to run alongside. (No live
      <code>ssh</code>? The <a routerLink="/profiles">first run</a> asks once, via a short wizard.)
    </p>

    <h3>3. Run your process as a service of the cluster</h3>
    <p>
      Something the cluster can <strong>call back</strong>: it answers to a name, like any deployed
      workload.
    </p>
    <app-code lang="bash">plug -s my-app:8080:3000 npm run start:dev</app-code>
    <p>
      Your command runs as a named member of the cluster: it answers to <code>my-app:8080</code>,
      forwarded to its local <code>:3000</code>, and in your code you address cluster services by
      name - <code>http://pdfbox:8080</code>, <code>mongodb:27017</code>. <kbd>Ctrl-C</kbd> stops
      your process; when the last one exits, your machine is back exactly as it was.
    </p>

    <h3>4. Run your process as a pure client</h3>
    <p>
      Something the cluster will <strong>never call back</strong> - a GUI database tool, a one-off
      script. It reaches services by name, but nothing is named and no port is reserved on the
      agent. One stance or the other, never both:
    </p>
    <p class="for-os">Commands below are for <ng-container *ngTemplateOutlet="osPicker" /></p>
    @if (os() === 'windows') {
      <app-code lang="bash">plug -c "/c/Program Files/MongoDB Compass/MongoDBCompass.exe"</app-code>
    } @else if (os() === 'macos') {
      <app-code
        lang="bash"
        >plug -c "/Applications/MongoDB Compass.app/Contents/MacOS/MongoDB Compass"</app-code
      >
    } @else {
      <app-code lang="bash">plug -c mongodb-compass</app-code>
    }
    <p>
      <strong>Give plug the program, not a launcher for it.</strong>
      @if (os() === 'macos') {
        <code>open -a Compass</code> is the reflex here, and it does not work: <code>open</code>
        asks LaunchServices to start the app and returns in about 50 milliseconds, with the app
        parented to <code>launchd</code>.
      } @else if (os() === 'windows') {
        <code>start</code> is the reflex here, and it does not work: it hands the program to the
        shell and returns at once, with the program parented to something else.
      } @else {
        <code>xdg-open</code> is the reflex here, and it does not work: it hands the program to the
        desktop session and returns at once, with the program parented to something else.
      }
      plug then sees your command finish, ends the session and takes the tunnel down, while the
      program it was supposed to serve is only just starting - and being nobody's child of plug, it
      cannot be attributed to a cluster either. Same reason as
      <a routerLink="/cli"><code>--dockerrun</code></a> further down: what you launched handed the
      real work to another process manager. Point plug at the executable itself, as above, and it
      stays for as long as the program runs.
    </p>

    <h3>5. Run a container as a member of the cluster</h3>
    <p>
      Testing an image before you deploy it needs one more word, because prefixing
      <code>docker</code> with plug cannot work: plug carries the traffic of the process it
      launches, and that process is the docker CLI, which posts a request to a socket and exits. The
      container is created by the docker <strong>daemon</strong>, which is nobody's child, so none
      of its traffic would go through the tunnel. <code>--dockerrun</code> puts plug in the network
      the container will use instead:
    </p>
    <app-code lang="bash"># your image reaches cluster services by name, unmodified
plug -c --dockerrun docker run --rm my-image

# and can answer to one, exactly like a process would
plug -s my-api:8080:8080 --dockerrun docker run --rm my-image</app-code>
    <p>
      Same on all three systems, because a container is a Linux environment on every one of them.
      <code>docker run</code> only, and in the foreground: see the
      <a routerLink="/cli">CLI reference</a> for the limits and the two environment variables that
      go with it.
    </p>

    <h3>6. Let plug pick the local port</h3>
    <p>
      The cluster port is agreed in advance - it is what other workloads dial. The
      <strong>local</strong> one is nobody's business but yours, and pinning it is what makes two
      projects fight over <code>3000</code>. Name it instead, and plug picks a free one per session:
    </p>
    <app-code lang="bash">plug -s web:8080:PORT npm run dev -- --port=&#123;PORT&#125;</app-code>
    <p>
      <code>PORT</code> <strong>declares</strong> it - bare, because the third field of a
      <code>-s</code> can only ever be a port, so there is nothing to disambiguate.
      <code>&#123;PORT&#125;</code> <strong>references</strong> it in the command - braced, because
      argv is free text and a bare <code>PORT</code> would also rewrite
      <code>--transport=PORTAL</code>. Any name works, and the same name twice means one port:
    </p>
    <app-code lang="bash"># one process, two cluster names, one listener
plug -s web:80:PORT -s web-tls:443:PORT node server.js --listen=&#123;PORT&#125;</app-code>
    <p>
      The command line is the only channel - plug puts nothing in your process's environment. The
      two halves have to match, and plug says so at startup rather than let either mistake through:
      a <code>&#123;TOKEN&#125;</code> nothing declared reaches your command as a literal string, and
      a name nothing references allocates a port your process is never told about. Pinning still
      works, and still makes sense when something outside the session needs a stable address - a
      bookmarked URL, a debugger attach config.
    </p>

    <h3>Where to next</h3>
    <p>
      Something feels off? <code>plug doctor</code> checks everything plug touches - binaries,
      resolver state, the privileged service, each profile's cluster - and names the remedy next to
      each finding.
    </p>
    <p>
      Read the <a routerLink="/cli">CLI reference</a> for every command,
      <a routerLink="/how-it-works">How it works</a> for the mechanics,
      <a routerLink="/profiles">Profiles &amp; versions</a> for day-to-day usage and updates,
      <a routerLink="/swarm">Swarm</a> for the cluster side,
      <a routerLink="/security">Security model</a> before deploying anywhere sensitive, the
      <a routerLink="/coverage">Coverage matrix</a> for what works on which OS, and the
      <a routerLink="/roadmap">Roadmap</a> for what's coming.
    </p>
  `,
  styles: [
    `
      .head {
        display: flex;
        align-items: baseline;
        justify-content: space-between;
        flex-wrap: wrap;
        gap: 12px;
      }
      .head h2 {
        margin-bottom: 0;
      }
      /* One row of three, so the choice reads as a choice. Sticky would fight
         the page header on a short viewport; the switch sits with the title
         instead, where the reader is when they need it. */
      .os {
        display: inline-flex;
        border: 1px solid var(--border-color);
        border-radius: 8px;
        overflow: hidden;
      }
      .os button {
        appearance: none;
        border: 0;
        margin: 0;
        padding: 6px 14px;
        font: inherit;
        font-size: 0.9rem;
        color: var(--text-muted);
        background: var(--bg-secondary);
        cursor: pointer;
      }
      .os button + button {
        border-left: 1px solid var(--border-color);
      }
      .os button:hover {
        color: var(--text-primary);
      }
      .os button.on {
        color: var(--text-primary);
        background: var(--bg-primary);
        box-shadow: inset 0 -2px 0 var(--accent-blue);
      }
      /* The reminder line: the switch sits IN the sentence, so it reads as part
         of it rather than as a control someone parked there. */
      .for-os {
        display: flex;
        align-items: center;
        gap: 10px;
        color: var(--text-muted);
        font-size: 0.9rem;
      }
      .for-os .os button {
        padding: 4px 10px;
        font-size: 0.85rem;
      }
      .os button:focus-visible {
        outline: 2px solid var(--accent-blue);
        outline-offset: -2px;
      }
    `,
  ],
})
export class GettingStartedComponent {
  protected readonly oses = [
    { id: 'linux' as const, label: 'Linux' },
    { id: 'macos' as const, label: 'macOS' },
    { id: 'windows' as const, label: 'Windows' },
  ];
  protected readonly os = signal<Os>(guessOs());
}
