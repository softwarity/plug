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
      not a secret. Treat the agent's published port as an open door to the attached networks,
      and to what the agent can do in them on a developer's behalf: read a workload's environment
      and secret files, mount its volumes, point a name at a machine. The sections below list
      exactly that.
    </div>

    <h3>What this means, concretely</h3>
    <ul>
      <li>Anyone who can reach the agent port gets <strong>network-level access</strong> to every network the agent is attached to - DNS resolution included.</li>
      <li>Since 2.16 (the environment a takeover inherits, <code>--env-of</code>) and 2.20 (the live mount), the agent does more than relay packets. Whoever can reach the port can also, for any workload of the agent's namespace (Kubernetes) or of the attached stack (Docker, Swarm): read its <strong>environment and its mounted secret and config files</strong> as the workload itself sees them, <strong>mount its data volumes read-write</strong> for a session, and <strong>point its name</strong> at their own machine. A Secret the workload has already received as a variable or a file is readable this way, with no right on Secrets at all: the pod resolved it. What is <em>not</em> on offer is a shell. The helper behind the tunnel user is a <code>ForceCommand</code> exposing a fixed set of verbs with validated arguments (<code>serve-name</code>, <code>unserve-name</code>, <code>info</code>, <code>check-update</code>, <code>resolve</code>, <code>env-of</code>, <code>files-of</code>, <code>volumes-of</code>, <code>mount-volume</code>, <code>unmount-volume</code>, <code>mount-status</code>, <code>self-update</code>), and that validation is all that stands between the port and the grant described below.</li>
      <li>The reverse direction (<code>-s</code>, serving a local port to the cluster) opens exactly <strong>one local port</strong> to cluster workloads, for the session's lifetime - never general access to the dev machine. Note the boundary honestly: the listener is a port <strong>on the agent</strong>, so any workload that can reach the agent can reach it (the name is the intended path, not an ACL).</li>
      <li><strong>Names, environments and mounts need a broad grant on the cluster - read this.</strong> On Docker and Swarm the agent needs the Docker socket, which is root on the host: plug uses it to create and delete signpost containers (signpost <em>services</em> when the agent runs as a Swarm service), to read a workload's environment, to start the mount helper and to refresh its own deployment on <code>plug update</code>, but the capability it hands over is the whole API. On Kubernetes the manifest's Role is namespace-scoped and grants, in the agent's namespace: <code>services</code> get/list/create/delete/update/patch, <code>endpoints</code> get/create/update/delete, <code>endpointslices</code> list/deletecollection, <code>deployments</code> get/list/patch, <code>pods</code> get/list/create/delete, <code>pods/exec</code> get/create and <code>persistentvolumeclaims</code> get. The <a routerLink="/kubernetes">Kubernetes page</a> says which feature needs which rule. Read the pod rules for what they are: <code>pods/exec</code> is the right to run code in every pod of the namespace, and <code>pods create</code> the right to start a pod under any ServiceAccount of the namespace with any of its Secrets mounted. The agent uses them for <code>exec cat /proc/1/environ</code> and for the mount helper, nothing else; but an identity holding them has, in effect, every secret the namespace's pods have resolved, and <code>deployments patch</code> is not restricted to plug's own Deployment. This is not a small Role. Neither grant is optional: an agent that cannot provision names refuses to start, because that is what it is deployed for. Mount the socket, or apply the Role, only on a trusted dev cluster.</li>
      <li>Note <code>self-update</code> honestly: anyone who can reach the agent port can trigger a redeploy of the agent and, since <code>plug update &lt;tag&gt;</code>, choose <em>which tag</em> of that repository it lands on, an older release included. The repository itself is fixed by the deployment, so this is never an arbitrary image; but the version is caller-chosen, so a downgrade to a release with known issues is within reach of whoever can reach the port. Same trust boundary as the rest of plug - the agent port is not a public one.</li>
      <li>The data tunnel <strong>pins the agent's host key on first use</strong> (<code>~/.plug/known_hosts</code>). Because the agent regenerates its key on every start (<code>ssh-keygen -A</code> - not a secret here), a changed key is the normal after-a-restart case, so plug <strong>re-pins it and prints a one-line notice</strong> rather than blocking: the notice is the informative tripwire (a key change on a host you did <em>not</em> restart is worth a glance), without the chore of hand-editing <code>known_hosts</code> after every deploy. The one-shot install/download over the <code>get</code> user skips the check (<code>StrictHostKeyChecking=no</code>) - there is no client secret to protect there. For an agent whose key you keep stable, <code>PLUG_STRICT_HOSTKEY=1</code> turns the notice into a refusal: a changed key then ends the session and names the <code>known_hosts</code> line to remove.</li>
    </ul>

    <h3>The boundary is the port</h3>
    <p>
      Everything above follows from one fact: the tunnel key is public, so <strong>reaching the
      port is the whole authorization</strong>. On Kubernetes the manifest publishes a NodePort
      (<code>32222</code> by default) on every node, so the boundary is whatever can reach the
      nodes. <code>kubectl -n my-namespace port-forward svc/plug 2222:2222</code> reaches the same
      agent with <strong>no exposed port at all</strong>: the tunnel rides the API server and each
      developer's own kubeconfig gates it, which is the closest plug gets to a per-person check
      without <a routerLink="/meerkat">Meerkat</a>. Drop the NodePort from the manifest if that is
      your model. On Docker and Swarm the published port is the boundary: firewall it to your
      office or VPN range.
    </p>

    <h3>The download user (<code>get</code>)</h3>
    <p>
      The agent also serves the CLI binaries on the same port through a second, passwordless SSH
      user named <code>get</code>. It is <em>more</em> locked down than the tunnel user, not less:
    </p>
    <ul>
      <li>The server replaces whatever the client asks with a single script that can only print the version, <code>cat</code> a binary, or emit the install script - no shell, ever. There is no Unix account behind it and no shell in reach to fall back to.</li>
      <li>Forwarding is refused outright for <code>get</code>: it cannot open the network tunnel, only the <code>plug</code> account (public-key) can.</li>
      <li>Empty password is intentional and harmless here: there is nothing to protect behind it - the whole surface is "download a public binary".</li>
    </ul>
    <app-code lang="bash">ssh -p 2222 get@&lt;host&gt; install                         # returns the installer
ssh -p 2222 get@&lt;host&gt; cat /etc/shadow                  # ForceCommand ignores this</app-code>

    <h3>Where it is a fine trade-off</h3>
    <p>
      Internal <strong>development clusters</strong> on trusted networks (office LAN, VPN). The
      whole point of plug is frictionless onboarding: no key distribution, no account management -
      a colleague installs the CLI and is productive in one minute.
    </p>

    <h3>The rules</h3>
    <ul>
      <li><strong>Never</strong> publish the agent port on an untrusted or public network.</li>
      <li>Attach the agent <strong>only to the networks devs actually need</strong> - the networks list in the <a routerLink="/swarm">stack descriptor</a> is your scoping tool.</li>
      <li>Kubernetes: the Role's reach is the agent's namespace, every workload in it. Deploy the agent where the dev services live, and never in a namespace a production workload shares.</li>
      <li>Production clusters: don't. If you must debug against production-like data, use a dedicated staging cluster.</li>
      <li>Defense in depth is still available <em>around</em> plug: firewall the published port to your office/VPN CIDR at the host or cloud level, or reach it through <code>kubectl port-forward</code> and publish no port.</li>
    </ul>

    <app-code lang="text">reachable port  =  member of the attached networks
                +  the helper's verbs on the workloads in reach:
                   names, environment, secret files, volumes
        scope   =  the networks: list of the stack (Docker, Swarm)
                   the agent's namespace (Kubernetes), nothing more</app-code>

    <h3>If your threat model grows</h3>
    <p>
      The architecture doesn't change - only the transport hardens: generate a real keypair, bake
      the public half into your own agent image, distribute the private half to the team. The
      planned <a routerLink="/roadmap">API-gateway integration</a> goes further: the tunnel endpoint
      is enabled/disabled dynamically and inherits the gateway's own authentication.
    </p>
  `,
})
export class SecurityComponent {}
