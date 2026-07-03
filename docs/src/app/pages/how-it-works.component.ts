import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-how-it-works',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>How it works</h2>

    <p>
      plug is a thin, opinionated orchestration of proven pieces: an SSH transport to a tiny agent
      in the cluster, and an <a href="https://github.com/sshuttle/sshuttle" target="_blank"
      rel="noopener">sshuttle</a> tunnel that routes the cluster subnets — and DNS — through it.
    </p>

    <app-code lang="text">┌─ your laptop ──────────────┐         ┌─ swarm cluster ───────────────┐
│  plug &lt;cmd&gt;                │         │  plug-agent (alpine + sshd)   │
│   ├─ discovers subnets ────┼──ssh────┼─→ attached to overlay nets    │
│   ├─ sshuttle tunnel ──────┼──:2222──┼─→ relays traffic + DNS        │
│   └─ runs &lt;cmd&gt;            │         │   (resolver 127.0.0.11)       │
└────────────────────────────┘         └───────────────────────────────┘</app-code>

    <h3>The stages</h3>
    <ul>
      <li>
        <strong>Subnet discovery.</strong> plug opens an SSH session to the agent and asks for its
        interfaces and default route (<code>ip -o -4 addr show</code>). Every subnet the agent sits
        on is a candidate — <em>except</em> the one carrying the default route: that is the docker
        gateway bridge, services never live there. Result: exactly the overlay networks, with nobody
        ever typing a CIDR. A profile can still pin <code>subnets =</code> explicitly (that is also
        the Kubernetes escape hatch — see <a routerLink="/roadmap">Roadmap</a>).
      </li>
      <li>
        <strong>Tunnel + DNS.</strong> plug starts sshuttle towards the agent with the discovered
        subnets and <code>--dns</code>. From that moment, packets to overlay IPs are relayed by the
        agent, and DNS queries are resolved <em>on the agent side</em> — by the Swarm embedded
        resolver (<code>127.0.0.11</code>), the same one every container uses. That is why service
        names simply work.
      </li>
      <li>
        <strong>Your command runs.</strong> plug waits for the tunnel to be up, then starts your
        command as a child process with stdio passed through — pipes, colors and
        <kbd>Ctrl-C</kbd> behave exactly as without plug.
      </li>
      <li>
        <strong>Teardown.</strong> When your command exits (or you interrupt it), plug stops
        sshuttle and exits with your command's status code. Nothing stays behind — the tunnel's
        lifetime <em>is</em> the command's lifetime.
      </li>
    </ul>

    <h3>Design choices</h3>
    <ul>
      <li><strong>Session-scoped, machine-visible.</strong> While the tunnel is up, routing applies to the whole machine (that is how transparent redirection works) — but only for the cluster subnets, and only until your command exits.</li>
      <li><strong>stdin belongs to your process.</strong> Interactive prompts (wizard, profile choice) read <code>/dev/tty</code>, so <code>cat data.json | plug my-script</code> pipes cleanly into the child.</li>
      <li><strong>No daemon, no state.</strong> Each invocation is self-contained; two terminals can plug into two different clusters.</li>
      <li><strong>sudo once per session.</strong> sshuttle installs local packet-redirection rules (pf/iptables); that is the only privilege involved, on your side only.</li>
    </ul>

    <h3>Built with open source</h3>
    <p>plug stands on the shoulders of these projects — thank you:</p>
    <table>
      <thead>
        <tr><th>Dependency</th><th>Role</th><th>License</th></tr>
      </thead>
      <tbody>
        <tr><td><a href="https://github.com/sshuttle/sshuttle" target="_blank" rel="noopener">sshuttle</a></td><td>Transparent proxy — subnet routing and DNS forwarding over SSH ("poor man's VPN")</td><td>LGPL-2.1</td></tr>
        <tr><td><a href="https://www.openssh.com/" target="_blank" rel="noopener">OpenSSH</a></td><td>Transport between the CLI and the agent (client on your machine, <code>sshd</code> in the agent)</td><td>BSD</td></tr>
        <tr><td><a href="https://www.python.org/" target="_blank" rel="noopener">Python 3</a></td><td>Runs sshuttle's server half inside the agent</td><td>PSF</td></tr>
        <tr><td><a href="https://go.dev/" target="_blank" rel="noopener">Go</a></td><td>The CLI — one static binary per platform, no runtime dependencies</td><td>BSD</td></tr>
        <tr><td><a href="https://www.alpinelinux.org/" target="_blank" rel="noopener">Alpine Linux</a></td><td>Base of the ~15&nbsp;MB agent image</td><td>MIT</td></tr>
      </tbody>
    </table>

    <div class="callout">
      <strong>Why not mirrord / Telepresence?</strong> Both are excellent — for Kubernetes. plug
      exists because nothing equivalent existed for <strong>Docker Swarm</strong>, and because its
      transport (a dumb sshd + python container) is simple enough to embed later into other hosts —
      like an API gateway (see <a routerLink="/roadmap">Roadmap</a>).
    </div>
  `,
})
export class HowItWorksComponent {}
