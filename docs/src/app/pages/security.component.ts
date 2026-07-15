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
      <li>The reverse direction (<code>-s</code>, serving a local port to the cluster) opens exactly <strong>one local port</strong> to cluster workloads, for the session's lifetime — never general access to the dev machine. Note the boundary honestly: the listener is a port <strong>on the agent</strong>, so any workload that can reach the agent can reach it (the name is the intended path, not an ACL).</li>
      <li><strong>Dynamic name provisioning needs a broad grant on Docker — read this.</strong> To create <code>-s</code> names without a redeploy, the agent needs the Docker socket (Docker/Swarm) or a Services-only RBAC role (Kubernetes). The Kubernetes grant is tight and namespace-scoped (manage Services, nothing else). The Docker socket is <em>not</em> tight — it is root on the host; plug uses it only to create and delete signpost containers (or, when the agent runs as a Swarm service, signpost <em>services</em>), but the capability it hands over is broad, so mount it only on a trusted dev cluster (or skip it and declare names statically). The agent-side helper is itself locked down: it is the tunnel user's <code>ForceCommand</code>, exposing only <code>serve-name</code>/<code>unserve-name</code> with DNS-label-validated arguments — no shell.</li>
      <li>The data tunnel <strong>pins the agent's host key on first use</strong> (<code>~/.plug/known_hosts</code>). Because the agent regenerates its key on every start (<code>ssh-keygen -A</code> — not a secret here), a changed key is the normal after-a-restart case, so plug <strong>re-pins it and prints a one-line notice</strong> rather than blocking: the notice is the informative tripwire (a key change on a host you did <em>not</em> restart is worth a glance), without the chore of hand-editing <code>known_hosts</code> after every deploy. The one-shot install/download over the <code>get</code> user skips the check (<code>StrictHostKeyChecking=no</code>) — there is no client secret to protect there.</li>
    </ul>

    <h3>The download user (<code>get</code>)</h3>
    <p>
      The agent also serves the CLI binaries on the same port through a second, passwordless SSH
      user named <code>get</code>. It is <em>more</em> locked down than the tunnel user, not less:
    </p>
    <ul>
      <li>An OpenSSH <code>ForceCommand</code> replaces whatever the client asks with a single script that can only print the version, <code>cat</code> a binary, or emit the install script — no shell, ever.</li>
      <li>TCP/X11 forwarding is disabled for <code>get</code>: it cannot open the network tunnel, only the <code>plug</code> user (public-key) can.</li>
      <li>Empty password is intentional and harmless here: there is nothing to protect behind it — the whole surface is "download a public binary".</li>
    </ul>
    <app-code lang="bash">ssh -p 2222 get@&lt;host&gt; install                         # returns the installer
ssh -p 2222 get@&lt;host&gt; cat /etc/shadow                  # ForceCommand ignores this</app-code>

    <h3>Where it is a fine trade-off</h3>
    <p>
      Internal <strong>development clusters</strong> on trusted networks (office LAN, VPN). The
      whole point of plug is frictionless onboarding: no key distribution, no account management —
      a colleague installs the CLI and is productive in one minute.
    </p>

    <h3>The rules</h3>
    <ul>
      <li><strong>Never</strong> publish the agent port on an untrusted or public network.</li>
      <li>Attach the agent <strong>only to the networks devs actually need</strong> — the networks list in the <a routerLink="/swarm">stack descriptor</a> is your scoping tool.</li>
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
