import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';
import { FileComponent } from '../file/file.component';

@Component({
  selector: 'app-agent-kubernetes',
  imports: [CodeComponent, RouterLink, FileComponent],
  template: `
    <h2>Kubernetes</h2>

    <p>
      The <a routerLink="/swarm">same agent image</a> runs on Kubernetes - a small Alpine +
      <code>sshd</code> pod that sits inside the cluster and dials services on the CLI's behalf.
      Nothing Kubernetes-specific is baked in; only the way you deploy and reach it differs.
    </p>

    <h3>Deploy</h3>
    <p>
      Apply <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-k8s.yaml"
      target="_blank" rel="noopener">deploy/plug-k8s.yaml</a> in the target namespace. No subnet or
      CIDR is needed - the agent's <code>sshd</code> resolves service names via CoreDNS from inside
      the cluster, exactly like on Swarm.
    </p>
    <p>The full manifest - copy it or download <code>plug-k8s.yaml</code>, then apply:</p>
    <app-file src="assets/plug-k8s.yaml" download="plug-k8s.yaml" [preview]="14" [maxLines]="22" />
    <app-code lang="bash">kubectl -n my-namespace apply -f plug-k8s.yaml</app-code>

    <h3>Reaching it</h3>
    <ul>
      <li>
        <strong>NodePort</strong> - the manifest publishes one (default <code>32222</code>); point
        the <a routerLink="/profiles">profile</a> at any node's IP and that port.
      </li>
      <li>
        <strong><code>kubectl port-forward</code></strong> - an RBAC-gated tunnel with
        <strong>no exposed port</strong>: access is governed by each developer's own kubeconfig,
        which also softens the <a routerLink="/security">no-auth trade-off</a>.
      </li>
    </ul>
    <app-code lang="bash"># zero exposed port - the tunnel rides the API server, gated by your kubeconfig RBAC
kubectl -n my-namespace port-forward svc/plug 2222:2222</app-code>

    <div class="callout">
      Same agent, same contract: plug reaches only what the agent's namespace can resolve and route.
      Deploy it where the dev services live, and the rest of the cluster stays out of reach - see
      <a routerLink="/security">Security model</a>.
    </div>

    <h3>The name in the cluster</h3>
    <p>
      <code>plug -s &lt;name&gt;:&lt;cluster-port&gt;:&lt;local-port&gt; &lt;cmd&gt;</code> publishes the
      process in the cluster under a DNS name, for the lifetime of
      the session - <strong>no name pre-declared, no redeploy</strong>. On Kubernetes the name is a
      <strong>Service selecting the agent pod</strong>, and the agent creates and deletes it itself
      per session. The manifest above already grants exactly what that needs - a small,
      namespace-scoped RBAC role (manage Services, nothing else); it is part of the one deploy, so
      <code>-s</code> works out of the box. Apply the manifest as a whole: an agent that cannot
      manage Services <strong>refuses to start</strong>, rather than come up looking healthy and
      fail on the first <code>-s</code>.
    </p>
    <p>
      A dev runs <code>plug -s service1:8081:4200 npm start</code> - pods calling
      <code>http://service1:8081</code> land on their machine's <code>:4200</code>, and the Service
      is gone when the session ends (leftovers from a crashed session are swept on agent restart).
      plug verifies the full path at startup; the port closes with the session. A Service name is
      unique, so unlike Swarm there is no DNS round-robin - if the real <code>service1</code> is
      deployed it already owns the name, and plug <strong>takes it over</strong>: the existing
      Service is repointed at the agent (its original selector and ports saved in an annotation on
      the Service itself) and <strong>restored when the session ends</strong> - even across an
      agent restart. Note the pods themselves keep running (only the name is rerouted) - a parked
      k8s workload still consumes queues and runs its crons. A nice property of this design: the
      Service keeps its <strong>ClusterIP</strong> through park and restore, so cached DNS answers
      (a JVM caches ~30&thinsp;s) stay <em>valid</em> across the switch - kube-proxy reroutes
      underneath, and <em>new</em> connections reach your session immediately. No stale-IP window,
      unlike Swarm where the address behind the name changes. The mirror-image caveat: since the
      parked pods keep running, a caller holding a <strong>keep-alive connection</strong> opened
      before the switch keeps reaching the old pod until that connection closes or idles out
      (typically under two minutes) - where Swarm's stopped container kills them outright. The
      <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-k8s.yaml" target="_blank" rel="noopener">manifest</a>
      and the <a routerLink="/security">security model</a> spell out exactly what the grant allows.
    </p>

    <div class="callout">
      <strong>Cluster driven by Argo CD, Flux or Fleet?</strong> A controller that reconciles
      continuously undoes the takeover a few minutes in, so a name can stop pointing at your
      machine without anything failing loudly. One directive on the controller settles it - see
      <a routerLink="/continuous-deployment">CD &amp; GitOps</a>.
    </div>

    <p>
      The image, tags, how it also serves the CLI, and the under-the-hood notes are identical on
      every platform - see <a routerLink="/swarm">Swarm</a> for those.
    </p>
  `,
})
export class AgentKubernetesComponent {}
