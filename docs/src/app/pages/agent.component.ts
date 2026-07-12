import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-agent',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>Agent &amp; deployment</h2>

    <p>
      The agent is deliberately boring: a small Alpine image running just <code>sshd</code>. No
      state, no volume, no configuration. Its only job is to <em>be inside</em> the overlay networks
      and let <code>sshd</code> dial services on the CLI's behalf (<code>direct-tcpip</code>).
    </p>

    <h3>Deploy on Docker Swarm — in your stack (recommended)</h3>
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

    <h3>Kubernetes</h3>
    <p>
      Deploy <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-k8s.yaml"
      target="_blank" rel="noopener">deploy/plug-k8s.yaml</a> in the target namespace. No subnet or
      CIDR is needed — the agent's <code>sshd</code> resolves service names via CoreDNS from inside
      the cluster, exactly like on Swarm. Reach it via the NodePort, or
      <code>kubectl port-forward svc/plug 2222:2222</code> for an RBAC-gated tunnel with no
      exposed port.
    </p>
    <app-code lang="bash">kubectl -n my-namespace apply -f plug-k8s.yaml</app-code>

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
ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@&lt;host&gt; install | sh   # install (Linux/macOS)
ssh -n -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get@&lt;host&gt; install-windows | bash -s -- &lt;host&gt; 2222   # install (Windows, Git Bash)</app-code>
    <p>
      The version baked into the image (and stamped into every binary) is what the
      <a routerLink="/profiles">launcher</a> asks for to run the matching version. Released images
      carry a real <code>x.y.z</code>; <code>latest</code> carries <code>dev</code>.
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
export class AgentComponent {}
