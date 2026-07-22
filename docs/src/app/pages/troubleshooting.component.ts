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
