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
      The <a routerLink="/swarm">same agent image</a> runs on Kubernetes - a small Alpine pod
      running one static Go binary, sitting inside the cluster and dialing services on the CLI's
      behalf.
      Nothing Kubernetes-specific is baked in; only the way you deploy and reach it differs.
    </p>

    <h3>Deploy</h3>
    <p>
      Apply <a href="https://github.com/softwarity/plug/blob/main/deploy/plug-k8s.yaml"
      target="_blank" rel="noopener">deploy/plug-k8s.yaml</a> in the target namespace. No subnet or
      CIDR is needed - the agent resolves service names via CoreDNS from inside
      the cluster, exactly like on Swarm.
    </p>
    <p>The full manifest - copy it or download <code>plug-k8s.yaml</code>, then apply:</p>
    <app-file src="assets/plug-k8s.yaml" download="plug-k8s.yaml" [preview]="14" [maxLines]="22" />
    <app-code lang="bash">kubectl -n my-namespace apply -f plug-k8s.yaml</app-code>

    <h3>What the Role grants, rule by rule</h3>
    <p>
      One namespace-scoped Role, and every rule in it has a feature behind it. <strong>Apply the
      manifest as a whole, and re-apply it when you upgrade the agent</strong>: <code>plug update</code>
      moves the image and never the manifest, so a rule a newer agent needs is missing until you do -
      the agent says which at boot, and <code>plug doctor -p &lt;profile&gt;</code> says it from your
      terminal, with the one-line <code>kubectl patch</code> that grants that rule alone. If you deploy
      the agent through your own chart, mirror these rules there.
    </p>
    <table class="rules">
      <thead><tr><th scope="col">Rule</th><th scope="col">Why</th><th scope="col">Since</th><th scope="col">Without it, <code>doctor</code> says</th></tr></thead>
      <tbody>
        <tr><td><code>services</code> get, list, create, delete, update, patch</td><td><code>-s</code> creates the Service carrying the name and deletes it after; a takeover repoints an existing one and restores it.</td><td>always</td><td>the agent refuses to start</td></tr>
        <tr><td><code>endpoints</code> get, create, update, delete</td><td>a served name points at the ONE agent pod holding the session, through Endpoints the agent writes (no selector).</td><td>2.12.0</td><td>"endpoints grant"</td></tr>
        <tr><td><code>discovery.k8s.io/endpointslices</code> list, deletecollection</td><td>a taken-over Service keeps the EndpointSlice Kubernetes built for its pod; kube-proxy routes over every slice, so without this half the requests still reach the deployed pod. The agent deletes it at park.</td><td>2.20.1</td><td>"endpointslices grant", and "taken-over names" still doubled</td></tr>
        <tr><td><code>apps/deployments</code> get, list, patch</td><td><code>plug update</code> rolls the agent's own Deployment so the node re-pulls the tag.</td><td>2.9</td><td>update by hand</td></tr>
        <tr><td><code>pods</code> get, list · <code>pods/exec</code> get, create</td><td>a plugged process inherits the parked pod's environment (<code>exec cat /proc/1/environ</code>) and its mounted secret files.</td><td>2.16.0</td><td>"exec grant"</td></tr>
        <tr><td><code>pods</code> create, delete · <code>persistentvolumeclaims</code> get</td><td>the live mount: a helper pod beside the workload serves its PersistentVolumeClaim over SMB for the session; the claim is read to refuse a ReadWriteOncePod one with the reason.</td><td>2.20.0</td><td>the mount answers "cannot create pods"</td></tr>
      </tbody>
    </table>

    <h3>OpenShift and OKD</h3>
    <p>
      The same manifest, the same Role, and the mount works under the default <code>restricted</code>
      SCC, with nothing particular to it: the helper has one shape on every cluster. It runs with
      the uid and gid of the workload whose volume it serves, which the agent reads on the
      workload's own process (<code>exec cat /proc/1/status</code>, the <code>pods/exec</code> rule
      above) or, failing that, in its <code>securityContext</code>; it carries the workload's
      <code>fsGroup</code>, no capability, no privilege escalation, the runtime's seccomp profile.
      The helper listens on 1445 and the helper's Service answers on 445, the port the client is told.
      Files are written under the workload's uid, so what you save is what it reads back. On
      OpenShift and OKD that uid is one of the namespace's range, which is all the SCC asks of a
      pod that names its own; the same pod passes Pod Security <code>restricted</code> elsewhere.
      Two cases keep two capabilities (<code>SETUID</code>, <code>SETGID</code>), which a restricted admission refuses: a workload that runs as root, and
      one whose uid could be read nowhere; the helper then writes as the owner of the volume's root.
    </p>

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
      <strong>Service pointing at the agent pod that holds the session</strong>, and the agent
      creates and deletes it itself per session. It carries no selector: a selector would match
      every replica of the agent, while the session lives in exactly one pod, so the agent writes
      the Service's <strong>endpoints</strong> instead - one address, the right pod. The manifest
      above grants exactly what that needs (the rules are listed above, each with its reason); it is
      part of the one deploy, so
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
