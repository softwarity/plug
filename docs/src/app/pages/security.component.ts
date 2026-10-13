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
      exactly that. Unless the agent is deployed to <a routerLink="/security" fragment="admission">admit by approval</a>:
      then only the keys a member accepted get in.
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

    <h3 id="admission">Admission by approval</h3>
    <p>
      One line at deploy time, <code>PLUG_ADMISSION=invite</code>, and the agent lets in its
      <strong>members</strong> only. Nothing changes for a developer beyond reading one message:
      the install line is the same, plug gives the machine a key of its own the first time the
      agent asks for one, presents it, and says what to ask of whom.
    </p>
    <ul>
      <li><strong>The first key to connect becomes the first member</strong>, typically whoever
        deployed the agent and installed plug right after. The agent's log names it
        (<code>first member: &lt;name&gt; SHA256:… from &lt;ip&gt;</code>), and anyone who arrives
        later sees the members' names, so a key that got there first does not go unnoticed.</li>
      <li><strong>Every later key is recorded as a request, and refused.</strong> The newcomer's
        plug prints the line to send to a member:</li>
    </ul>
    <app-code lang="text">plug: demo admits its members by approval. Your request is registered.
      Ask a member to run:
        plug -p demo accept 9kcQ2m7xT4bW
      or to pick you in: plug -p demo requests
      Members: francois, alice</app-code>
    <ul>
      <li><strong>A member accepts or revokes</strong>, and nobody else can: the three commands
        ride the tunnel. <code>plug -p demo accept &lt;key&gt;</code> takes the key the newcomer's
        plug printed, the first 12 characters of its fingerprint (too long to forge a second key
        that starts the same). <code>plug -p demo requests</code> lists the keys waiting and
        accepts the ones picked, <code>plug -p demo members</code> lists the members and revokes
        the ones picked (<code>plug -p demo revoke &lt;key&gt;</code> in a script). A revoked member's open sessions end at once. The last member cannot be
        revoked: the next key would become the first.</li>
      <li><strong>Members are equal.</strong> Every member can accept and revoke. Telling who may
        invite from who may only use is a matter of identity, which is what the hosted version
        (below) brings.</li>
      <li><strong>A request is forgotten</strong> after 7 days without a new attempt, and at
        most 20 wait at once, the oldest dropped first: anyone who reaches the port can add to
        the list, and it stays one a member can read. A machine that tries again keeps its one
        request, with the date of its latest attempt.</li>
      <li><strong>An offered key is never recorded.</strong> A request is written only once the
        client has signed with the key, so nobody holding a colleague's public half can put it on
        the list.</li>
    </ul>
    <p>
      The members live where the orchestrator keeps state: a Secret named
      <code>plug-members</code> on Kubernetes (the manifest creates it empty, and the Role reaches
      that one name, nothing else), a Swarm config the agent keeps by itself on Swarm, a volume at
      <code>/var/lib/plug/members</code> on Compose. <strong>An agent that cannot read its members
      does not start</strong>: it never falls back to letting anyone in. Deleting the Secret, the
      configs or the volume resets them, and the next key to connect becomes the first member,
      which is also the way back when every member has lost their key.
    </p>
    <p>
      What it closes is the open door the rest of this page describes: the port is no longer the
      whole authorization. What it does not change is what a member can do once in, which is
      everything above, and the traffic itself, which was already encrypted by SSH.
    </p>

    <h3>The download user (<code>get</code>)</h3>
    <p>
      The agent also serves the CLI binaries on the same port through a second, passwordless SSH
      user named <code>get</code>. It is <em>more</em> locked down than the tunnel user, not less:
    </p>
    <ul>
      <li>Whatever the client asks is handed to the agent binary itself, which can only print the version, send a binary with its digest, or emit the install script - no shell, ever. There is no Unix account behind it, and no shell in the image at all: the agent image holds <code>plug-agent</code> and nothing else that runs, so a request has no interpreter to reach, and an image scanner finds no shell or C library to report.</li>
      <li>Forwarding is refused outright for <code>get</code>: it cannot open the network tunnel, only the <code>plug</code> account (public-key) can.</li>
      <li>Empty password is intentional and harmless here: there is nothing to protect behind it - the whole surface is "download a public binary".</li>
    </ul>
    <app-code lang="bash">ssh -p 2222 get@&lt;host&gt; install                         # returns the installer
ssh -p 2222 get@&lt;host&gt; cat /etc/shadow                  # ForceCommand ignores this</app-code>

    <h3>Verifying the image</h3>
    <p>
      Every published image is signed with <a href="https://docs.sigstore.dev/">cosign</a>, and
      no key. There is no public key to fetch: the signature carries the identity of the GitHub
      Actions workflow that pushed the image, <code>ci.yml</code> of
      <code>softwarity/plug</code> on <code>main</code>, and that identity is what you check.
    </p>
    <app-code lang="bash">cosign verify \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \\
  --certificate-identity-regexp '^https://github\\.com/softwarity/plug/\\.github/workflows/ci\\.yml&#64;refs/heads/main$' \\
  docker.io/softwarity/plug:latest</app-code>
    <p>
      The same command verifies every tag: a release (<code>x.y.z</code>, <code>x.y</code>,
      <code>x</code>), <code>latest</code>, and their <code>-hosted</code> counterparts. A workflow
      on another branch, or another file, does not pass. Versions up to 2.25.0 carry no signature.
    </p>
    <p>
      What is signed is the <strong>digest</strong>, recursively: the manifest list and each
      per-architecture image under it. A node that pulled the arm64 image verifies what it
      actually runs, and an admission policy (Kyverno, Sigstore's policy-controller) that pins
      digests finds a signature on the one it pinned.
    </p>
    <p>
      The CLI binaries inside the image are signed too, separately, with plug's release key: a
      launcher checks that signature before it runs a core it downloaded from the agent, so the
      image signature is for whoever deploys the agent, and the binary signature for whoever
      runs <code>plug</code>.
    </p>

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

    <h3>For a shared cluster: the hosted version</h3>
    <p>
      Standalone, plug is meant for a <strong>private network</strong>: whoever reaches the agent's
      port is in. Where a cluster is shared, or that port cannot stay private, the answer is the
      <strong>hosted</strong> version, the same agent embedded in a gateway that knows who people
      are. <a routerLink="/meerkat">Meerkat</a> is one.
    </p>
    <ul>
      <li>
        <strong>Each developer has an identity.</strong> They create a personal key with
        <code>plug keygen</code> and register its public half in the gateway's own interface, where
        it is tied to their name.
      </li>
      <li>
        <strong>Only declared identities get in.</strong> A key the gateway has not been given
        opens nothing. Access is given and withdrawn per person, in the gateway, like any other
        right it manages.
      </li>
      <li>
        <strong>Every session has a name on it.</strong> Which service is plugged, by whom, since
        when: the cluster can say it.
      </li>
    </ul>
    <p>
      The CLI is the same and so is everything it does. What changes is who may use it, and that
      the gateway, not the network, decides.
    </p>
  `,
})
export class SecurityComponent {}
