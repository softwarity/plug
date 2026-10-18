import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { FileComponent } from '../file/file.component';

@Component({
  selector: 'app-install',
  imports: [CodeComponent, FileComponent, RouterLink],
  styles: [
    `
      .toc {
        columns: 2 220px;
        margin: 8px 0 20px;
      }
      h3 {
        margin-top: 36px;
      }
    `,
  ],
  template: `
    <h2>Install</h2>
    <p>
      Two pieces: one <strong>agent</strong> per cluster, and the <code>plug</code> CLI on each
      developer's machine, installed from that agent. The agent is the same image everywhere; only
      the way it is deployed, and the right it needs to publish a name, change with the platform.
    </p>
    <ul class="toc">
      <li><a routerLink="/install" fragment="compose">Docker Compose</a></li>
      <li><a routerLink="/install" fragment="swarm">Docker Swarm</a></li>
      <li><a routerLink="/install" fragment="kubernetes">Kubernetes</a></li>
      <li><a routerLink="/install" fragment="openshift">OpenShift and OKD</a></li>
      <li><a routerLink="/install" fragment="several-stacks">One agent for several stacks</a></li>
      <li><a routerLink="/install" fragment="cli">The CLI, on each machine</a></li>
    </ul>
    <p>
      Whatever the platform, the agent lets in whoever reaches its port until you set
      <code>PLUG_ADMISSION: invite</code>, and a session reaches what the agent reaches. Read the
      <a routerLink="/security">security model</a> before deploying it on a shared network.
    </p>

    <h3 id="compose">Docker Compose</h3>
    <p>Add the agent to the stack you want to reach. It joins the stack's network by itself:</p>
    <app-file src="assets/plug-service.yml" download="plug-service.yml" [initial]="'opened'" [preview]="16" />
    <ul>
      <li>
        <strong>The Docker socket is required</strong>: the agent creates your <code>-s</code> name
        as a small signpost container. It is root on the host; mount it only on a host you trust.
        Without it the agent does not start.
      </li>
      <li>
        With <code>PLUG_ADMISSION: invite</code>, mount the <code>plug-members</code> volume the
        comment shows, or the agent does not start.
      </li>
      <li>
        The port (<code>2222</code> here) is a convention: publish any, and give it to the
        <a routerLink="/profiles">profile</a>.
      </li>
    </ul>

    <h3 id="swarm">Docker Swarm</h3>
    <p>
      The same service, deployed with the stack (<code>docker stack deploy</code>). Two
      differences:
    </p>
    <ul>
      <li>
        The signpost is a Swarm <strong>service</strong>, so the agent runs on a
        <strong>manager</strong>, as one replica (the <code>deploy:</code> block above). The
        signpost joins the stack's overlay whether or not it is attachable.
      </li>
      <li>
        With <code>PLUG_ADMISSION: invite</code>, the agent keeps its members in a Swarm config by
        itself: nothing to mount.
      </li>
    </ul>
    <p>
      Through the routing mesh the agent answers on any node's address. Details:
      <a routerLink="/swarm">Docker, Compose and Swarm</a>.
    </p>

    <h3 id="kubernetes">Kubernetes</h3>
    <p>Apply the manifest in the namespace where the services you work on live:</p>
    <app-file src="assets/plug-k8s.yaml" download="plug-k8s.yaml" [preview]="14" [maxLines]="22" />
    <app-code lang="bash">kubectl -n my-namespace apply -f plug-k8s.yaml</app-code>
    <ul>
      <li>
        <strong>No socket</strong>: a namespace-scoped Role lets the agent create a Service for your
        name, repoint one it takes over, and read a workload's environment and volumes. Each rule
        and its reason: <a routerLink="/kubernetes">Kubernetes and OpenShift</a>.
      </li>
      <li>
        Reach it on the NodePort the manifest publishes (<code>32222</code>), or with no exposed
        port, through the API server and each developer's own kubeconfig:
        <app-code lang="bash">kubectl -n my-namespace port-forward svc/plug 2222:2222</app-code>
      </li>
      <li>
        <strong>Re-apply the manifest when you upgrade the agent</strong>: <code>plug update</code>
        moves the image, not the Role, and <code>plug doctor</code> names any rule a newer agent
        is missing.
      </li>
    </ul>

    <h3 id="openshift">OpenShift and OKD</h3>
    <p>
      The same manifest and the same Role, admitted by the default <code>restricted-v2</code> SCC
      with nothing to grant: the agent runs as the uid the cluster picks, never root, and asks for
      no capability. CI runs it on MicroShift built from OKD, installs plug from it and opens
      sessions through it. Details: <a routerLink="/kubernetes" fragment="openshift">OpenShift and OKD</a>.
    </p>

    <h3 id="several-stacks">One agent for several stacks</h3>
    <p>
      On Docker or Swarm, a single agent can cover several stacks: deploy
      <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-stack.yml" target="_blank" rel="noopener">deploy/plug-stack.yml</a>
      and list their networks. The networks it is attached to are exactly what sessions reach.
    </p>
    <app-code lang="bash">curl -fsSLO https://raw.githubusercontent.com/softwarity/plug/main/deploy/plug-stack.yml
# edit the networks: section, then:
docker stack deploy -c plug-stack.yml plug</app-code>

    <h3 id="cli">The CLI, on each machine</h3>
    <p>
      From the cluster, in one line: the agent serves the binary for your system. The install
      grants plug its privilege once; no later run needs it.
    </p>
    <p>Linux and macOS:</p>
    <app-code lang="bash">ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get&#64;&lt;cluster-host&gt; install | sh</app-code>
    <p>
      Windows, from Git Bash (it ships with
      <a href="https://git-scm.com/download/win" target="_blank" rel="noopener">Git for Windows</a>):
    </p>
    <app-code lang="bash">cluster=&lt;cluster-host&gt;
ssh -n -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null get&#64;$cluster install-windows \\
  | bash -s -- $cluster 2222</app-code>
    <p>
      The install does not check the agent's host key; plug pins it on the first connection after.
      The binaries are signed, and plug verifies the signature before granting itself privilege
      (<a routerLink="/security">security model</a>). Then run your first command:
      <a routerLink="/getting-started">Getting started</a>.
    </p>
  `,
})
export class InstallComponent {}
