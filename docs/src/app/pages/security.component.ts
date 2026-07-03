import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-security',
  imports: [CodeComponent, RouterLink],
  template: `
    <h2>Security model</h2>

    <div class="callout">
      <strong>There is deliberately no authentication.</strong> The SSH keypair is committed to the
      repository and embedded in every <code>plug</code> binary. It is a <em>transport detail</em>,
      not a secret. Treat the agent's published port as an open door to the attached overlay
      networks.
    </div>

    <h3>What this means, concretely</h3>
    <ul>
      <li>Anyone who can reach the agent port gets <strong>network-level access</strong> to every overlay network the agent is attached to — DNS resolution included.</li>
      <li>They do <em>not</em> get shell access to your services, volumes or the Docker API: the agent relays packets, nothing else. Exposure is exactly "being on those networks".</li>
      <li>SSH host keys are not verified (<code>StrictHostKeyChecking=no</code>): with no client secret to protect, MITM adds nothing an attacker could not already do by reaching the port.</li>
    </ul>

    <h3>The download user (<code>get</code>)</h3>
    <p>
      The agent also serves the CLI binaries on the same port through a second, passwordless SSH
      user named <code>get</code>. It is <em>more</em> locked down than the tunnel user, not less:
    </p>
    <ul>
      <li>An OpenSSH <code>ForceCommand</code> replaces whatever the client asks with a single script that can only <code>cat</code> a binary (or print the version) to stdout — no shell, ever.</li>
      <li>TCP/X11 forwarding is disabled for <code>get</code>: it cannot open the network tunnel, only the <code>plug</code> user (public-key) can.</li>
      <li>Empty password is intentional and harmless here: there is nothing to protect behind it — the whole surface is "download a public binary".</li>
    </ul>
    <app-code lang="bash">ssh -p 2222 get@&lt;host&gt; $(uname -s)-$(uname -m) > plug   # only ever returns a binary
ssh -p 2222 get@&lt;host&gt; cat /etc/shadow                 # ForceCommand ignores this</app-code>

    <h3>Where it is a fine trade-off</h3>
    <p>
      Internal <strong>development clusters</strong> on trusted networks (office LAN, VPN). The
      whole point of plug is frictionless onboarding: no key distribution, no account management —
      a colleague installs the CLI and is productive in one minute.
    </p>

    <h3>The rules</h3>
    <ul>
      <li><strong>Never</strong> publish the agent port on an untrusted or public network.</li>
      <li>Attach the agent <strong>only to the networks devs actually need</strong> — the networks list in the <a routerLink="/agent">stack descriptor</a> is your scoping tool.</li>
      <li>Production clusters: don't. If you must debug against production-like data, use a dedicated staging cluster.</li>
      <li>Defense in depth is still available <em>around</em> plug: firewall the published port to your office/VPN CIDR at the host or cloud level.</li>
    </ul>

    <app-code lang="text">reachable port  =  member of the attached overlay networks
        scope   =  the networks: list of the stack, nothing more</app-code>

    <h3>If your threat model grows</h3>
    <p>
      The architecture doesn't change — only the transport hardens: generate a real keypair, bake
      the public half into your own agent image, distribute the private half to the team. The
      planned <a routerLink="/roadmap">API-gateway integration</a> goes further: the tunnel endpoint
      is enabled/disabled dynamically and inherits the gateway's own authentication.
    </p>
  `,
})
export class SecurityComponent {}
