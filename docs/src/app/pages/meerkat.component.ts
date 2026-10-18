import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-meerkat',
  imports: [CodeComponent, RouterLink],
  template: `
    <h2>Meerkat</h2>

    <p>
      <a href="https://softwarity.github.io/meerkat/" target="_blank" rel="noopener">Meerkat</a> is
      an app-gateway that drives plug. Its Enterprise edition integrates the two, and what it adds
      is an <strong>identity</strong>: sessions stop being anonymous.
    </p>

    <h3>Who is plugged in, by name</h3>
    <p>
      On its own, plug lets in whoever reaches the agent, or, by approval, the keys its members
      accepted. Through Meerkat, a developer holding the
      <code>dev</code> ability deposits their <strong>own public key</strong>, which is tied to
      their name and known to the agent. They then authenticate on the agent's port, download the
      CLI and open sessions as themselves - every session attributed to a person the gateway
      knows, with the rights it gives them.
    </p>
    <p>
      That changes what the cluster can tell you. A plugged service is already a shared state:
      whoever enters the cluster uses the services as they currently are, and someone's laptop may
      well be answering for one of them. Meerkat does not create that situation - it makes it
      <strong>visible and attributed</strong>: which service is plugged, and by whom. Everyone
      signing in to the application is told before it opens, and a strip says it again on every
      page, live. A bug report can then say which code answered, rather than guess.
    </p>

    <h3>Who may plug at all</h3>
    <p>
      Consumption of that state is indiscriminate: there is no role for the reader, because
      everyone reads the same cluster. The control is on who may <strong>produce</strong> it, and
      the gateway decides it the same way on every installation, a production one included:
    </p>
    <ul>
      <li>
        <strong>Per installation, two switches, two owners.</strong> The application's
        administrator turns on developer mode; the infrastructure administrator opens the developer
        tunnel, which ships closed. The tunnel runs only when both are on, and while it is closed a
        developer is not even offered a key to deposit.
      </li>
      <li>
        <strong>Per person.</strong> The <code>dev</code> ability is what lets someone deposit a key
        and plug at all. Removing the key, the ability or either switch closes the door at the next
        connection: nothing was issued that would outlive it.
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
      - is the same plug, so nothing you learn here has to be relearned there. How a gateway
      embeds the agent is on the <a routerLink="/integration">Integration</a> page.
    </p>
    <p>
      Installing it ends with the one step a gateway requires: the machine's own key. The install
      command the gateway shows names the profile after its application, and the installer says
      what to run, then where the key goes, in the gateway's own words:
    </p>
    <app-code lang="text">plug: this cluster lets in only the keys it knows. Give this machine its own, once:
        plug keygen -p my-app
        plug pubkey -p my-app
      and register the key it prints in your dev profile</app-code>

    <p>
      This page is a summary and will grow. See
      <a href="https://softwarity.github.io/meerkat/" target="_blank" rel="noopener">the Meerkat
      documentation</a> for the gateway itself, and the <a routerLink="/roadmap">Roadmap</a> for
      what is still ahead.
    </p>
  `,
})
export class MeerkatComponent {}
