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
      The agent is deliberately boring: an Alpine image (~15&nbsp;MB) running <code>sshd</code>,
      with <code>python3</code> (sshuttle's server half) and <code>iproute2</code> (subnet
      discovery). No state, no volume, no configuration. Its only job is to <em>be inside</em> the
      overlay networks and relay.
    </p>

    <h3>Deploy on Docker Swarm</h3>
    <p>The descriptor is versioned in the repo — download, adapt, deploy:</p>
    <app-code lang="bash">curl -fsSLO https://raw.githubusercontent.com/softwarity/plug/main/deploy/plug-stack.yml
docker stack deploy -c plug-stack.yml plug</app-code>

    <app-code lang="yaml"># plug-stack.yml
services:
  agent:
    image: docker.io/softwarity/plug-agent:latest
    ports:
      - "2222:22"
    networks:
      - app_net          # ← list EVERY overlay network holding services
    deploy:              #   you want to reach from your laptop
      replicas: 1

networks:
  app_net:
    external: true</app-code>

    <div class="callout">
      <strong>The networks list is the contract.</strong> plug can only reach services on networks
      the agent is attached to — that is also your scoping tool: attach the agent to the dev
      networks only, and production stays out of reach by construction. See
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
      <a href="https://hub.docker.com/r/softwarity/plug-agent" target="_blank" rel="noopener">Docker Hub</a>.
    </p>

    <h3>Under the hood</h3>
    <ul>
      <li><code>sshd</code> accepts a single unprivileged user (<code>plug</code>), public-key only.</li>
      <li>Host keys are generated at container start — connections use <code>StrictHostKeyChecking=no</code>, consistent with the <a routerLink="/security">no-auth model</a>.</li>
      <li>No TCP forwarding tricks: sshuttle multiplexes everything over the SSH session itself.</li>
      <li>The container logs its attached networks at startup — <code>docker service logs plug_agent</code> is your first debugging stop.</li>
    </ul>
  `,
})
export class AgentComponent {}
