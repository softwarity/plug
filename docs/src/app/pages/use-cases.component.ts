import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { CtaComponent } from '../cta/cta.component';

@Component({
  selector: 'app-use-cases',
  imports: [CodeComponent, CtaComponent, RouterLink],
  styles: [
    `
      .toc {
        columns: 2 260px;
        margin: 8px 0 24px;
      }
      .toc li {
        break-inside: avoid;
      }
      .diagram {
        margin: 18px 0 6px;
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
      .without {
        border-left: 3px solid var(--accent-yellow);
        background: var(--accent-yellow-tint);
        padding: 10px 14px;
        border-radius: 0 8px 8px 0;
        margin: 14px 0 8px;
      }
      .without strong {
        color: var(--accent-yellow);
      }
      h3 {
        margin-top: 40px;
      }
      h3.group {
        margin-top: 56px;
        padding-top: 16px;
        border-top: 1px solid var(--border-color);
      }
    `,
  ],
  template: `
    <h2>Use cases</h2>
    <p>
      Stories from a developer's day, each with the commands that tell it. They all rest on the same
      thing: your process runs on your machine and is a member of the cluster, in both directions
      (<a routerLink="/how-it-works">how it works</a>). The names below, <code>demo</code> for the
      profile, <code>orders</code> or <code>web</code> for a service, are examples.
    </p>
    <ul class="toc">
      <li><a routerLink="/use-cases" fragment="first-day">First day on a project</a></li>
      <li><a routerLink="/use-cases" fragment="debug">The bug only the cluster shows</a></li>
      <li><a routerLink="/use-cases" fragment="front">A front end on the laptop, the back end in the cluster</a></li>
      <li><a routerLink="/use-cases" fragment="review">QA tries the feature before the pull request</a></li>
      <li><a routerLink="/use-cases" fragment="data">The real data, in your own tools</a></li>
      <li><a routerLink="/use-cases" fragment="two-clusters">Staging and pre-production, side by side</a></li>
      <li><a routerLink="/use-cases" fragment="ai">An AI agent that knows the cluster</a></li>
      <li><a routerLink="/use-cases" fragment="gitops">A cluster driven by Argo CD</a></li>
      <li><a routerLink="/use-cases" fragment="meerkat">Teams, with Meerkat</a></li>
    </ul>

    <!-- ===================================================================== -->
    <h3 id="first-day">First day on a project</h3>
    <p>
      You join a team. Its dev cluster admits its members by approval, and the tech lead gives you
      one thing: the agent's address. You paste the install line with it:
    </p>
    <app-code lang="bash">ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get&#64;10.0.4.20 install demo | sh</app-code>
    <p>
      The installer puts plug on your machine, then presents the machine to the cluster, which
      records the request and says what to ask for:
    </p>
    <app-code lang="text">plug: demo admits its members by approval. Your request is registered.
      Ask a member to run:
        plug -p demo accept 9kcQ2m7xT4bW
      or to pick you in: plug -p demo requests
      Members: alice, bob</app-code>
    <p>You paste that line to the tech lead, who runs it from their own machine:</p>
    <app-code lang="bash">plug -p demo accept 9kcQ2m7xT4bW</app-code>
    <p>Two minutes later, you clone the service you were hired for and start it:</p>
    <app-code lang="bash">git clone git&#64;git.example.com:shop/orders.git && cd orders
plug -p demo -s orders:8080:3000 npm run start:dev</app-code>
    <p>
      Your <code>orders</code> runs on your laptop and calls <code>users</code>,
      <code>payments</code>, <code>postgres</code> and <code>kafka</code> by their names, the team's
      own, with this morning's data. The rest of the cluster calls your <code>orders</code> in turn.
    </p>
    <div class="diagram">
      <img src="assets/usecase-first-day.svg" width="900" height="360" loading="lazy" alt="Three steps: at 9:00 you install plug from the cluster and the installer prints the accept command; at 9:02 the tech lead runs it; at 9:05 you clone orders and run it with plug, connected to the team's users, payments, postgres and kafka." />
    </div>
    <p class="without">
      <strong>Without plug:</strong> a cluster to rebuild on the laptop, from compose files that
      drift from the real one, a seed script for data nobody trusts, and a few days before the first
      useful request. Then every service the team adds is one more to run locally.
    </p>

    <!-- ===================================================================== -->
    <h3 id="debug">The bug only the cluster shows</h3>
    <p>
      A request fails in the dev cluster and nowhere else: it takes the real data, the real
      configuration, the real neighbours. You start <code>orders</code> under your debugger, in its
      place:
    </p>
    <app-code lang="bash">plug -p demo -s orders:8080:3000 npm run start:debug</app-code>
    <ul>
      <li>
        The deployed <code>orders</code> is <strong>parked</strong> for the session. Whatever calls
        <code>orders</code> in the cluster, <code>web</code>, a consumer, a cron job, now lands on
        your process: you reproduce the failing request from the real front end, and the breakpoint
        hits.
      </li>
      <li>
        Your process gets the <strong>environment</strong> of the <code>orders</code> it replaces,
        secrets included, and its <strong>volumes</strong>, mounted live. It behaves as the deployed
        one does, with nothing copied into a <code>.env</code>. <code>--no-env DB_URL</code> keeps
        one variable at your local value.
      </li>
      <li>
        <strong>Ctrl-C</strong> and the deployed <code>orders</code> answers again. Nothing was
        redeployed, before or after.
      </li>
    </ul>
    <div class="diagram">
      <img src="assets/usecase-takeover.svg" width="900" height="420" loading="lazy" alt="Inside the cluster the deployed orders is parked; web's calls to orders go through the plug agent and the SSH tunnel to your machine, where orders runs under a debugger with the deployed one's environment and volume, and calls payments and postgres by name." />
    </div>
    <p>The same session, in other shapes:</p>
    <ul>
      <li>
        <strong>A service that does not exist yet.</strong>
        <code>plug -p demo -s pricing:8080:3000 npm run start:dev</code> gives the cluster a name it
        did not have: the other services can call <code>pricing</code> before it has a manifest.
      </li>
      <li>
        <strong>The image, as it will ship.</strong>
        <code>plug -p demo -s orders:8080:8080 --dockerrun docker run --rm orders:my-branch</code>
        runs your container, unmodified, as the cluster member.
      </li>
      <li>
        <strong>The service's files.</strong> From another terminal, <code>plug mounts</code> says
        where its volumes are on your machine: open them in your editor, they are the live ones.
      </li>
    </ul>
    <p>Running it from the IDE: see <a routerLink="/build-tools">From your build tool</a>.</p>

    <!-- ===================================================================== -->
    <h3 id="front">A front end on the laptop, the back end in the cluster</h3>
    <p>
      You work on the front end only. Its dev server proxies <code>/api</code> to
      <code>http://api:8080</code>, the address it has in the cluster, and you start it as a pure
      client:
    </p>
    <app-code lang="bash">plug -p demo -c npm run dev</app-code>
    <p>
      The dev server is the process plug runs, so it is the one that resolves the cluster's names;
      your browser talks to it on <code>localhost</code> as usual. No URL per environment, no
      port-forward to keep open, and the back end is the whole one, not a mock.
    </p>

    <!-- ===================================================================== -->
    <h3 id="review">QA tries the feature before the pull request</h3>
    <p>
      Your feature works on your machine. Before you open the pull request, someone has to try it:
      QA, or the business who asked for it. They know the test platform's address, and nothing about
      your machine, which sits behind a firewall, a NAT, a VPN. You serve the front end from your
      branch:
    </p>
    <app-code lang="bash">plug -p test -s web:80:4200 npm start</app-code>
    <p>
      They open the test platform as they always do, with its usual address and its usual sign-in:
      the ingress sends <code>web</code> to your process. The tunnel was opened by your machine,
      outbound, so nothing has to route to it. A remark? You fix it, save, and their next reload
      has it. Once they are happy: Ctrl-C, commit, push, pull request.
    </p>
    <div class="diagram">
      <img src="assets/usecase-review.svg" width="900" height="380" loading="lazy" alt="The reviewer's browser opens the test platform; the ingress sends web to the plug agent, which carries it through the SSH tunnel, opened outbound by your machine behind its firewall, to your branch of web." />
    </div>
    <ul>
      <li>
        <strong>Everyone on the platform gets your version</strong> for the session: a name is
        served by one process at a time. Say so in the team's channel, or let
        <a routerLink="/use-cases" fragment="meerkat">Meerkat</a> tell them.
      </li>
      <li>
        A dev server that checks the <code>Host</code> header refuses the platform's address
        until it is allowed: <code>server.allowedHosts</code> in Vite, <code>--allowed-hosts</code>
        on <code>ng serve</code>.
      </li>
    </ul>
    <p class="without">
      <strong>Without plug:</strong> a build and a deployment to the test platform for every round
      of remarks, or a preview environment per branch to build and keep running.
    </p>

    <!-- ===================================================================== -->
    <h3 id="data">The real data, in your own tools</h3>
    <p>Your database client, your GUI, your scripts reach the cluster's data by name:</p>
    <app-code lang="bash">plug -p demo -c psql -h postgres -U orders
plug -p demo -c mongodb-compass</app-code>
    <p>
      A one-off script can run with a service's own credentials, read from the deployed workload,
      without parking it and without copying a password anywhere:
    </p>
    <app-code lang="bash">plug -p demo -c --env-of orders python scripts/backfill.py</app-code>
    <p>
      This is the cluster's data, not a copy: what the script writes, the team reads.
    </p>

    <!-- ===================================================================== -->
    <h3 id="two-clusters">Staging and pre-production, side by side</h3>
    <p>
      The bug shows on pre-production and not on staging. You run the same commit against both, in
      two terminals, and compare:
    </p>
    <app-code lang="bash">plug -p staging -s orders:8080:PORT npm run start:dev -- --port=&#123;PORT&#125;
plug -p preprod -s orders:8080:PORT npm run start:dev -- --port=&#123;PORT&#125;</app-code>
    <p>
      <code>PORT</code> lets plug pick a free local port for each session and write it where
      <code>&#123;PORT&#125;</code> stands, so the two do not collide. Each process resolves the
      names of its own cluster. More in
      <a routerLink="/profiles">Profiles &amp; versions</a>.
    </p>

    <!-- ===================================================================== -->
    <h3 id="ai">An AI agent that knows the cluster</h3>
    <p>One line gives your coding agent plug's view of every cluster this machine knows:</p>
    <app-code lang="bash">claude mcp add --scope user plug -- plug mcp</app-code>
    <p>
      Ask it why <code>orders</code> does not reach its database when you plug it, and it reads
      <code>doctor</code> check by check, the environment the deployed <code>orders</code> runs
      with, whether a name exists in the cluster, then proposes the fix instead of guessing. Other
      agents and the tools: <a routerLink="/mcp">MCP</a>.
    </p>

    <!-- ===================================================================== -->
    <h3 id="gitops">A cluster driven by Argo CD</h3>
    <p>
      Argo CD keeps every object equal to the repository, so it puts back the Service your session
      repointed, a few minutes in, and the name quietly stops answering on your machine. Tell it,
      once, to leave the two fields plug changes alone, and plug sessions live beside GitOps.
      The setting for Argo CD, Flux, Fleet and Crossplane:
      <a routerLink="/continuous-deployment">CD &amp; GitOps</a>.
    </p>

    <!-- ===================================================================== -->
    <h3 id="meerkat" class="group">Teams, with Meerkat</h3>
    <p>
      When the cluster sits behind <a routerLink="/meerkat">Meerkat</a>, the agent runs inside the
      gateway and every session belongs to a person the gateway knows.
    </p>

    <h4>Arriving, and leaving</h4>
    <p>
      An administrator gives your account the <code>dev</code> capability. You install plug with the
      command the gateway shows, give this machine its own key, and deposit the public half on your
      profile:
    </p>
    <app-code lang="bash">ssh -p 22222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get&#64;gateway.example.com install my-app | sh
plug keygen -p my-app
plug pubkey -p my-app</app-code>
    <p>
      One key per workstation: a second machine deposits a second key, and removing one closes that
      machine alone. When you leave the project, removing your key or your capability closes the
      door at the next connection. No shared key to rotate, no certificate to wait out.
    </p>

    <h4>Everybody sees who serves what</h4>
    <p>
      Back to the <a routerLink="/use-cases" fragment="review">review on the test platform</a>, through
      Meerkat. The tester signs in, and before the application opens, the gateway names what is
      substituted and by whom. Then a strip says it again on every page, live:
    </p>
    <div class="diagram">
      <img src="assets/usecase-meerkat.svg" width="900" height="330" loading="lazy" alt="An illustration: at sign-in the gateway lists what is substituted, checkout by Alice; then a strip on every page of the application says checkout, by Alice." />
    </div>
    <p>
      A bug report then says which code answered: Alice's branch, not the deployed
      <code>checkout</code>. And a name already served is refused to a second developer, with the
      name of who holds it.
    </p>

    <h4>Opening the door</h4>
    <p>
      Two switches, two owners: the application's administrator turns on developer mode, and the
      infrastructure administrator opens the developer tunnel, which ships closed. The tunnel runs
      only when both are on, and taking either back closes it.
    </p>

    <app-cta link="/getting-started">Set it up</app-cta>
  `,
})
export class UseCasesComponent {}
