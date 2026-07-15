import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-agent-swarm',
  imports: [CodeComponent, RouterLink],
  template: `
    <h2>Agent &amp; Swarm</h2>

    <p>
      The agent is deliberately boring: a small Alpine image running just <code>sshd</code>. No
      state, no volume, no configuration. Its only job is to <em>be inside</em> the overlay networks
      and let <code>sshd</code> dial services on the CLI's behalf (<code>direct-tcpip</code>). The
      same image runs on <a routerLink="/kubernetes">Kubernetes</a> too.
    </p>

    <h3>In your stack (recommended)</h3>
    <p>
      The simplest deployment is no deployment at all: add the service to the stack you want to
      plug into. It joins that stack's network automatically — nothing else to declare:
    </p>
    <app-code lang="yaml"># your existing docker-compose / stack file
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports:
      - "2222:22"

  # ... your services ...</app-code>

    <h3>Standalone — one agent for several stacks</h3>
    <p>
      To cover multiple stacks with a single agent, deploy
      <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-stack.yml" target="_blank"
      rel="noopener">deploy/plug-stack.yml</a> and list their overlay networks explicitly:
    </p>
    <app-code lang="bash">curl -fsSLO https://raw.githubusercontent.com/softwarity/plug/main/deploy/plug-stack.yml
# edit the networks: section, then:
docker stack deploy -c plug-stack.yml plug</app-code>

    <div class="callout">
      <strong>The networks are the contract.</strong> plug can only reach services on networks the
      agent is attached to — that is also your scoping tool: put the agent in the dev stacks only,
      and production stays out of reach by construction. See
      <a routerLink="/security">Security model</a>.
    </div>

    <h3>Port</h3>
    <p>
      <code>2222</code> is a convention, not a requirement. Publish any port you like and put it in
      the <a routerLink="/profiles">profile</a>. In Swarm's routing mesh the agent is reachable via
      <em>any</em> node's IP.
    </p>

    <h3>Image &amp; tags</h3>
    <table>
      <thead>
        <tr><th>Tag</th><th>Meaning</th></tr>
      </thead>
      <tbody>
        <tr><td><code>latest</code></td><td>Last build of <code>main</code></td></tr>
        <tr><td><code>x.y.z</code> / <code>x.y</code></td><td>Released versions (created by the Release workflow, matching the GitHub Release of the CLI)</td></tr>
      </tbody>
    </table>
    <p>
      Multi-arch manifest — <code>linux/amd64</code> and <code>linux/arm64</code>, each built on
      native runners (no QEMU). Hosted on
      <a href="https://hub.docker.com/r/softwarity/plug" target="_blank" rel="noopener">Docker Hub</a>.
    </p>

    <h3>It also serves the CLI</h3>
    <p>
      The image is multi-stage: it compiles the CLI binaries into <code>/opt/plug/bin/</code> and
      writes <code>/opt/plug/VERSION</code>. The <code>get</code> user serves a fixed, tiny set and
      nothing else — the agent <code>version</code>, a named binary (<code>&lt;os&gt;-&lt;arch&gt;</code>),
      the <code>wintun</code> driver DLL for Windows, and two installers: <code>install</code> for
      Linux/macOS (binaries <strong>embedded</strong>, picked with <code>uname</code>) and
      <code>install-windows</code> (a Git Bash script):
    </p>
    <app-code lang="bash"># the agent regenerates its host key each start (not a secret here), so skip the check
cluster=&lt;host&gt;
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@$cluster install | sh   # install (Linux/macOS)
ssh -n -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@$cluster install-windows | bash -s -- $cluster 2222   # install (Windows, Git Bash)</app-code>
    <p>
      The version baked into the image (and stamped into every binary) is what the
      <a routerLink="/profiles">launcher</a> asks for to run the matching version. Released images
      carry a real <code>x.y.z</code>; <code>latest</code> carries <code>dev</code>.
    </p>

    <h3>The name in the cluster</h3>
    <p>
      <code>plug -s &lt;name&gt;:&lt;cluster-port&gt;:&lt;local-port&gt; &lt;cmd&gt;</code> publishes the process
      in the cluster under a DNS name, for the lifetime of the session. A dev runs
      <code>plug -s service1:8081:4200 npm start</code> and any workload calling
      <code>http://service1:8081</code> lands on their machine's <code>:4200</code>
      — <strong>no name pre-declared, no redeploy</strong>. When the session ends the name is gone.
    </p>
    <p>
      For that the agent needs to create the DNS name on the fly, which means the Docker socket.
      It is <strong>required</strong> — mount it on the plug service:
    </p>
    <app-code lang="yaml">services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports:
      - "2222:22"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock   # required: the agent creates your -s name</app-code>
    <p>
      With the socket, plug creates the name when the session starts and removes it when the
      session ends — nothing to pre-declare. Without it, use a name you declared yourself (a network
      alias on the plug service). Either way, if the name can't be reached plug stops with the
      reason — never a silent no-op — and one name is served by one session at a time.
    </p>
    <p>
      On <strong>Swarm</strong>, run the agent on a manager node as a single replica. Your existing
      network needs no change (a non-attachable overlay is fine):
    </p>
    <app-code lang="yaml">services:
  plug:
    # …
    deploy:
      replicas: 1                              # required for plug -s
      placement:
        constraints: [node.role == manager]   # required for plug -s (a single-node swarm is one)</app-code>
    <div class="callout">
      <strong>The socket is root on the host.</strong> Mount it only on a cluster you trust (the same
      trust plug's no-auth transport already assumes). Too much? Skip it and declare the name
      yourself as a static alias.
    </div>
    <p>
      Installed a launcher before <code>-s</code> existed? Put <code>-s</code> after
      <code>-p</code>/<code>--host</code> — older launchers pass flags they don't know to the core.
    </p>

    <h3>Under the hood</h3>
    <ul>
      <li>Two SSH users: <code>plug</code> (public-key, runs the tunnel) and <code>get</code> (passwordless, <code>ForceCommand</code>-locked to serving a binary) — see <a routerLink="/security">Security model</a>.</li>
      <li>Host keys are generated at container start — connections use <code>StrictHostKeyChecking=no</code>, consistent with the <a routerLink="/security">no-auth model</a>.</li>
      <li>The <code>plug</code> user has <code>AllowTcpForwarding</code> on: the CLI splices each flow onto an SSH <code>direct-tcpip</code> channel, so <code>sshd</code> does the real dials from inside the cluster.</li>
      <li>The container logs its attached networks at startup — <code>docker service logs &lt;stack&gt;_plug</code> is your first debugging stop.</li>
    </ul>
  `,
})
export class AgentSwarmComponent {}
