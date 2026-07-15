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

    <h3>The name in the cluster</h3>
    <p>
      <code>plug -s &lt;name&gt;:&lt;cluster-port&gt;:&lt;local-port&gt; &lt;cmd&gt;</code> publishes the
      process in the cluster under a DNS name, for the lifetime of
      the session — <strong>no name pre-declared, no redeploy</strong>. On Kubernetes the name is a
      <strong>Service selecting the agent pod</strong>, and the agent creates and deletes it itself
      per session. The manifest above already grants exactly what that needs — a small,
      namespace-scoped RBAC role (manage Services, nothing else); it is part of the one deploy, so
      <code>-s</code> works out of the box.
    </p>
    <p>
      A dev runs <code>plug -s service1:8081:4200 npm start</code> — pods calling
      <code>http://service1:8081</code> land on their machine's <code>:4200</code>, and the Service
      is gone when the session ends (leftovers from a crashed session are swept on agent restart).
      plug verifies the full path at startup; the port closes with the session. A Service name is
      unique, so unlike Swarm there is no DNS round-robin — if the real <code>service1</code> is
      deployed it already owns the name, so remove it while you serve yours. The
      <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-k8s.yaml" target="_blank" rel="noopener">manifest</a>
      and the <a routerLink="/security">security model</a> spell out exactly what the grant allows.
    </p>

    <p>
      The image, tags, how it also serves the CLI, and the under-the-hood notes are identical on
      every platform — see <a routerLink="/swarm">Agent &amp; Swarm</a> for those.
    </p>
  `,
})
export class AgentKubernetesComponent {}
