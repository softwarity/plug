import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-security',
  imports: [CodeComponent, RouterLink],
  styles: [
    `
      .modes {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
        gap: 12px;
        margin: 16px 0 20px;
      }
      .modes h3 {
        margin: 0 0 4px;
      }
      .modes .tag {
        font-size: 0.8rem;
        color: var(--text-secondary);
      }
      .modes dl {
        margin: 10px 0 0;
      }
      .modes dt {
        color: var(--text-primary);
        font-weight: 600;
        font-size: 0.85rem;
        margin-top: 10px;
      }
      .modes dd {
        margin: 2px 0 0;
      }
    `,
  ],
  template: `
    <h2>Security model</h2>
    <p>
      Who may open a session on a cluster is decided in one of three ways. Pick the one that fits
      who can reach the agent.
    </p>

    <div class="modes">
      <div class="card">
        <h3>Open</h3>
        <span class="tag">the default</span>
        <dl>
          <dt>Who gets in</dt>
          <dd>Whoever reaches the agent's port.</dd>
          <dt>Why that holds</dt>
          <dd>
            The tunnel key ships in every plug binary: it proves the software, not a person. Behind
            an office network or a VPN, reaching the port already means being one of the team, and a
            colleague is at work a minute after the install, with no key to hand out and no account
            to create.
          </dd>
          <dt>For</dt>
          <dd>A dev cluster on a network you trust.</dd>
          <dt>To set up</dt>
          <dd>Nothing: it is how the agent starts.</dd>
        </dl>
      </div>
      <div class="card">
        <h3>By approval</h3>
        <span class="tag">PLUG_ADMISSION=invite</span>
        <dl>
          <dt>Who gets in</dt>
          <dd>
            The members. The first key to connect becomes the first member; any other is refused and
            kept as a request until a member accepts it.
          </dd>
          <dt>Why</dt>
          <dd>
            Reaching the port is no longer enough. A stranger who reaches it gets a request in a
            queue, and nothing else.
          </dd>
          <dt>For</dt>
          <dd>A network you do not fully trust, or with more people on it than should use the cluster.</dd>
          <dt>To set up</dt>
          <dd>One variable on the agent.</dd>
        </dl>
      </div>
      <div class="card">
        <h3>Hosted</h3>
        <span class="tag">with <a routerLink="/meerkat">Meerkat</a></span>
        <dl>
          <dt>Who gets in</dt>
          <dd>The people a gateway knows. Each one registers a key there, under their own name.</dd>
          <dt>Why</dt>
          <dd>
            Rights are given and withdrawn per person, and every session carries a name: which
            service is plugged, by whom, since when.
          </dd>
          <dt>For</dt>
          <dd>A cluster several teams share, or where every session must be attributed.</dd>
          <dt>To set up</dt>
          <dd>The agent runs inside the gateway.</dd>
        </dl>
      </div>
    </div>
    <p>
      In all three, the traffic is encrypted by SSH, and what a session can do once in is the same
      (<a routerLink="/security" fragment="reach">below</a>). The mode only decides who gets that far.
    </p>

    <h3>Moving from one to another</h3>

    <h4>From open to by approval</h4>
    <ol>
      <li>
        <strong>Set <code>PLUG_ADMISSION: invite</code> on the agent and redeploy it.</strong> The
        <a routerLink="/kubernetes">Kubernetes manifest</a> and the
        <a routerLink="/swarm">service snippet</a> carry the line, commented. The members are kept in
        the <code>plug-members</code> Secret the manifest creates on Kubernetes, in a config the agent
        makes by itself on Swarm, and on Compose in a volume to mount at
        <code>/var/lib/plug/members</code>, without which the agent does not start.
      </li>
      <li>
        <strong>Connect first.</strong> The first key to connect becomes the first member, and that
        should be you:
        <app-code lang="bash">plug -p demo members</app-code>
      </li>
      <li>
        <strong>Let the others in.</strong> Their next plug command gives their machine a key of its
        own, records their request, and tells them what to ask:
        <app-code lang="text">plug: demo admits its members by approval. Your request is registered.
      Ask a member to run:
        plug -p demo accept 9kcQ2m7xT4bW
      or to pick you in: plug -p demo requests
      Members: alice, bob</app-code>
        Any member runs the line, or <code>plug -p demo requests</code> to pick from the waiting list.
      </li>
    </ol>

    <h4>From by approval back to open</h4>
    <p>
      Remove the variable and redeploy. The members stay where they are, unread: set the variable
      again and they are the members again. To start over with a new first member, delete the
      Secret, the config or the volume first.
    </p>

    <h4>To hosted</h4>
    <p>
      Hosted is not a setting of the agent you deployed: the agent runs inside the gateway, which
      serves its own CLI. Each developer installs plug with the command the gateway shows, gives the
      machine its key, and registers the key it prints in their gateway profile:
    </p>
    <app-code lang="bash">plug keygen -p my-app
plug pubkey -p my-app</app-code>
    <p>
      The members of an agent by approval do not carry over: the gateway decides who is in. Once
      everyone has moved, remove the agent you deployed. More on the <a routerLink="/meerkat">Meerkat page</a>.
    </p>

    <h3 id="reach">What a session can do once in</h3>
    <p>The same in every mode:</p>
    <ul>
      <li><strong>Reach the networks the agent is attached to</strong>, names included.</li>
      <li>
        For any workload in reach (the agent's namespace on Kubernetes, the attached stack on Docker
        and Swarm): <strong>read its environment and its mounted secret and config files</strong>,
        <strong>mount its data volumes</strong> read-write, and <strong>point its name</strong> at the
        developer's machine.
      </li>
      <li><strong>Redeploy the agent</strong> at another tag of its image, an older one included.</li>
    </ul>
    <p>
      There is no shell: the agent answers a fixed set of verbs, with their arguments checked. What
      bounds a session is what the agent reaches, so attach it only to the networks developers need,
      and deploy it in a namespace no production workload shares.
    </p>

    <h3 id="admission">Admission by approval in detail</h3>
    <ul>
      <li><strong>The first member is named.</strong> The agent's log says who it is
        (<code>first member: &lt;name&gt; SHA256:… from &lt;ip&gt;</code>), and anyone who arrives
        later sees the members' names, so a key that got there first does not go unnoticed.</li>
      <li><strong>Only a member accepts or revokes.</strong> <code>plug -p demo accept &lt;key&gt;</code>
        takes the first 12 characters of the newcomer's fingerprint, too many to forge a second key
        that starts the same. <code>plug -p demo members</code> lists the members and revokes the
        ones picked (<code>plug -p demo revoke &lt;key&gt;</code> in a script). A revoked member's
        open sessions end at once. The last member cannot be revoked: the next key would become the
        first.</li>
      <li><strong>Members are equal.</strong> Every member can accept and revoke. Telling who may
        invite from who may only use is a matter of identity, which is what hosted brings.</li>
      <li><strong>A request is forgotten</strong> after 7 days without a new attempt, and at most 20
        wait at once, the oldest dropped first: anyone who reaches the port can add to the list, and
        it stays one a member can read. A machine that tries again keeps its one request.</li>
      <li><strong>An offered key is never recorded.</strong> A request is written only once the
        client has signed with the key, so nobody holding a colleague's public half can put it on
        the list.</li>
      <li><strong>An agent that cannot read its members does not start.</strong> It never falls back
        to letting anyone in. Deleting the Secret, the config or the volume resets the members, and
        the next key to connect becomes the first: that is also the way back when every member has
        lost their key.</li>
    </ul>

    <h3>Details</h3>

    <h4>The port, in open mode</h4>
    <p>
      Open, reaching the port is the whole authorization, so the port is the boundary. On Kubernetes
      the manifest publishes a NodePort (<code>32222</code> by default) on every node: the boundary
      is whatever can reach the nodes. <code>kubectl -n my-namespace port-forward svc/plug 2222:2222</code>
      reaches the same agent with no exposed port at all, each developer's kubeconfig gating it;
      drop the NodePort from the manifest if that is your model. On Docker and Swarm, firewall the
      published port to your office or VPN range.
    </p>

    <h4>What the agent is granted on the cluster</h4>
    <p>
      Names, environments and mounts need a broad grant. On Docker and Swarm the agent needs the
      Docker socket, which is root on the host: it creates and removes signposts, reads a workload's
      environment, starts the mount helper and refreshes its own deployment, but the capability it
      holds is the whole API. On Kubernetes the Role is namespace-scoped, and includes
      <code>pods/exec</code> and <code>pods create</code>: in effect, every secret the namespace's
      pods have resolved. The <a routerLink="/kubernetes">Kubernetes page</a> says which feature
      needs which rule. Neither grant is optional, so mount the socket or apply the Role on a dev
      cluster only.
    </p>

    <h4>Serving a local port (<code>-s</code>)</h4>
    <p>
      The reverse direction opens one local port to the cluster's workloads, for the session's
      lifetime, never general access to the developer's machine. The listener is on the agent, so
      any workload that reaches the agent reaches it: the name is the intended path, not an ACL.
    </p>

    <h4>The agent's host key</h4>
    <p>
      plug pins the agent's host key on first use (<code>~/.plug/known_hosts</code>). The agent makes
      a new one on every start, so a changed key is the normal case after a restart: plug pins the
      new one and prints a one-line notice, worth a glance when you did not restart the agent.
      <code>PLUG_STRICT_HOSTKEY=1</code> turns the notice into a refusal, for an agent whose key you
      keep stable.
    </p>

    <h4>The download user (<code>get</code>)</h4>
    <p>
      The agent serves the CLI on the same port through a passwordless user, <code>get</code>, which
      can only print the version, send a binary with its digest, or emit the install script. It has
      no shell (the image holds none) and cannot open the tunnel. There is nothing behind it but
      public binaries.
    </p>
    <app-code lang="bash">ssh -p 2222 get@&lt;host&gt; install                         # returns the installer
ssh -p 2222 get@&lt;host&gt; cat /etc/shadow                  # ignored</app-code>

    <h4>Verifying the image</h4>
    <p>
      Every published image is signed with <a href="https://docs.sigstore.dev/">cosign</a>, with no
      key to fetch: the signature carries the identity of the workflow that pushed the image,
      <code>ci.yml</code> of <code>softwarity/plug</code> on <code>main</code>, and that is what you
      check.
    </p>
    <app-code lang="bash">cosign verify \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \\
  --certificate-identity-regexp '^https://github\\.com/softwarity/plug/\\.github/workflows/ci\\.yml&#64;refs/heads/main$' \\
  docker.io/softwarity/plug:latest</app-code>
    <p>
      The same command verifies every tag, <code>-hosted</code> included. What is signed is the
      digest of the manifest list and of each per-architecture image, so a node verifies what it
      actually runs. The CLI binaries inside are signed too, with plug's release key: a launcher
      checks that signature before it runs a core it downloaded from the agent.
    </p>

    <h3>The rules</h3>
    <ul>
      <li><strong>Never</strong> publish the agent's port on a public network, whatever the mode.</li>
      <li>On a network you do not fully trust, deploy the agent by approval.</li>
      <li>Attach the agent <strong>only to the networks developers need</strong>: the networks list in the <a routerLink="/swarm">stack descriptor</a> is the scoping tool.</li>
      <li>On Kubernetes, deploy the agent where the dev services live, never in a namespace a production workload shares.</li>
      <li>Production clusters: don't. Use a dedicated staging cluster.</li>
    </ul>
  `,
})
export class SecurityComponent {}
