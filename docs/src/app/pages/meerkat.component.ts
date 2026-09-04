import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-meerkat',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Meerkat</h2>

    <p>
      <a href="https://softwarity.github.io/meerkat/" target="_blank" rel="noopener">Meerkat</a> is
      an app-gateway that drives plug. Its Enterprise edition integrates the two, and what it adds
      is an <strong>identity</strong>: sessions stop being anonymous.
    </p>

    <h3>Who is plugged in, by name</h3>
    <p>
      On its own, plug authenticates with a key. Through Meerkat, a developer holding the
      <code>dev</code> ability deposits their <strong>own public key</strong>, which is tied to
      their name and known to the agent. They then authenticate on the agent's port, download the
      CLI and open sessions as themselves - every session attributed to a person rather than to a
      shared secret.
    </p>
    <p>
      That changes what the cluster can tell you. A plugged service is already a shared state:
      whoever enters the cluster uses the services as they currently are, and someone's laptop may
      well be answering for one of them. Meerkat does not create that situation - it makes it
      <strong>visible and attributed</strong>: which service is plugged, by whom, since when. A bug
      report can then be stamped with the exact composition of the cluster at the moment of the
      test, rather than with a guess.
    </p>

    <h3>Where plugging is allowed at all</h3>
    <p>
      Consumption of that state is indiscriminate - there is no role for the reader, because
      everyone reads the same cluster. The control is on its <strong>production</strong>, at two
      levels:
    </p>
    <ul>
      <li>
        <strong>Per environment.</strong> The gateway exposes the tunnel endpoint, or it does not:
        yes on dev, no on production. The prohibition is an <em>absence</em>, which is the strongest
        form it can take - nothing to bypass, nothing to audit.
      </li>
      <li>
        <strong>Per person.</strong> The <code>dev</code> ability is what lets someone deposit a key
        and plug at all.
      </li>
    </ul>
    <p>
      One name, one session stays the rule, as it is without Meerkat: a name already taken is
      refused rather than shared. What Meerkat changes is that the refusal can say who holds it.
    </p>

    <h3>The hosted CLI</h3>
    <p>
      A gateway that embeds the agent serves its own flavour of the CLI, published alongside the
      standalone one and signed the same way:
    </p>
    <app-code lang="text">softwarity/plug:&lt;version&gt;           # standalone: the agent you deploy yourself
softwarity/plug:&lt;version&gt;-hosted    # for a gateway that embeds the agent</app-code>
    <p>
      The hosted flavour carries key enrolment and leaves updates to the gateway, which owns the
      version it serves. Everything else - the <a routerLink="/cli">CLI</a>, the
      <a routerLink="/how-it-works">data path</a>, the <a routerLink="/security">security model</a>
      - is the same plug, so nothing you learn here has to be relearned there.
    </p>

    <p>
      This page is a summary and will grow. See
      <a href="https://softwarity.github.io/meerkat/" target="_blank" rel="noopener">the Meerkat
      documentation</a> for the gateway itself, and the <a routerLink="/roadmap">Roadmap</a> for
      what is shipped and what is still ahead.
    </p>
  `,
})
export class MeerkatComponent {}
