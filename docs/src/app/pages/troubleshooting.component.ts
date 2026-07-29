import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-troubleshooting',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Troubleshooting</h2>

    <p>
      First reflex, always: <code>plug doctor</code>. It checks everything plug touches and prints
      the remedy next to each finding — and can open a pre-filled (redacted) GitHub issue if you
      want to report one. See <a routerLink="/cli">the CLI reference</a>.
    </p>
    <app-code lang="bash">plug doctor</app-code>

    <h3>The agent container will not start</h3>
    <p>
      It prints what it is missing and exits: no Docker socket mounted, or no Kubernetes RBAC. That
      is deliberate. plug is deployed to plug services into the cluster, and creating a name takes
      that access — an agent that cannot do it would look healthy right up to your first
      <code>-s</code>. Add the mount (or apply <code>plug-k8s.yaml</code>) and redeploy; the message
      carries the exact stack-file lines.
    </p>

    <h3><code>-s</code> starts instantly, then warns that nothing reached the name</h3>
    <p>
      The mapping is armed the moment your command starts — proving the path end to end takes as
      long as the cluster needs to schedule the name (seconds, sometimes a minute on a loaded Swarm),
      and that wait is not charged to your command. The proof runs in the background and only speaks
      up if it fails.
    </p>
    <p>
      When it does, the name never carried traffic. Look cluster-side, where the message points:
      <code>docker service ps plug-sp-&lt;name&gt;</code> or <code>kubectl get svc &lt;name&gt;</code>.
      A signpost stuck in <em>Preparing</em>, a task restarting in a loop, or another workload
      holding the name all look like this. The session keeps running, so a name that comes up late
      still works.
    </p>
    <p>
      If every launch sits at the slow end, the cause is usually the daemon rather than plug: a Swarm
      manager carrying a long history of dead tasks and stopped containers schedules noticeably
      slower. <code>docker system prune</code> and a restart bring it back down.
    </p>

    <h3>After switching between the deployed service and your session, the app lags behind</h3>
    <p>
      You stop a session and the deployed service takes the name back — or the other way around —
      and for a while the app answers errors, then comes back "by itself". Nothing is broken:
      somewhere between the caller and the name, <strong>a cache is holding on to the old
      address</strong>. This shows up mostly after several switches in a row, or after
      <code>plug update</code> rolled the agent. Callers that keep connections open — an API
      gateway, typically — hold on the longest.
    </p>
    <p>
      Two ways out: <strong>wait a little</strong> (the cache expires on its own), or refresh the
      caller and reconnect instantly:
    </p>
    <app-code lang="bash">docker service update --force your-gateway</app-code>
    <p>
      On <strong>Kubernetes</strong> this does not happen: the takeover repoints the existing
      Service, so the address behind the name survives the switch — see
      <a routerLink="/kubernetes">the Kubernetes page</a>.
    </p>

    <h3>Your command crashed mid-session — is it plug?</h3>
    <p>
      When the process you launched dies on its own (a dev server tripping over its own build
      cache is a classic — an Angular one recovers with <code>rm -rf .angular/cache</code>), plug
      is just the messenger: it closes the session cleanly and the deployed service takes the name
      back. Telling them apart is easy — plug's own lines are prefixed <code>[plug]</code>;
      a stack trace from your runtime (node, java, python…) is your app's. Relaunch your command
      and the name is yours again.
    </p>

    <h3>A name that exists nowhere answers "unknown host"</h3>
    <p>
      That is intended (since 2.2): plug asks the cluster before answering, so a typo or a
      not-yet-deployed service fails fast and clearly, instead of timing out on a phantom address.
      If the name should exist, check the service is deployed — or served by a running
      <code>-s</code> session.
    </p>
  `,
})
export class TroubleshootingComponent {}
