import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-agent-kubernetes',
  imports: [CodeComponent, RouterLink],
  template: `
    <h2>Agent &amp; Kubernetes</h2>

    <p>
      The <a routerLink="/swarm">same agent image</a> runs on Kubernetes — a small Alpine +
      <code>sshd</code> pod that sits inside the cluster and dials services on the CLI's behalf.
      Nothing Kubernetes-specific is baked in; only the way you deploy and reach it differs.
    </p>

    <h3>Deploy</h3>
    <p>
      Apply <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-k8s.yaml"
      target="_blank" rel="noopener">deploy/plug-k8s.yaml</a> in the target namespace. No subnet or
      CIDR is needed — the agent's <code>sshd</code> resolves service names via CoreDNS from inside
      the cluster, exactly like on Swarm.
    </p>
    <app-code lang="bash">kubectl -n my-namespace apply -f plug-k8s.yaml</app-code>

    <h3>Reaching it</h3>
    <ul>
      <li>
        <strong>NodePort</strong> — the manifest publishes one (default <code>32222</code>); point
        the <a routerLink="/profiles">profile</a> at any node's IP and that port.
      </li>
      <li>
        <strong><code>kubectl port-forward</code></strong> — an RBAC-gated tunnel with
        <strong>no exposed port</strong>: access is governed by each developer's own kubeconfig,
        which also softens the <a routerLink="/security">no-auth trade-off</a>.
      </li>
    </ul>
    <app-code lang="bash"># zero exposed port — the tunnel rides the API server, gated by your kubeconfig RBAC
kubectl -n my-namespace port-forward svc/plug 2222:2222</app-code>

    <div class="callout">
      Same agent, same contract: plug reaches only what the agent's namespace can resolve and route.
      Deploy it where the dev services live, and the rest of the cluster stays out of reach — see
      <a routerLink="/security">Security model</a>.
    </div>

    <p>
      The image, tags, how it also serves the CLI, and the under-the-hood notes are identical on
      every platform — see <a routerLink="/swarm">Agent &amp; Swarm</a> for those.
    </p>
  `,
})
export class AgentKubernetesComponent {}
