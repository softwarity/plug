import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-continuous-deployment',
  imports: [CodeComponent, RouterLink],
  template: `
    <h2>Continuous deployment &amp; GitOps</h2>

    <p>
      If your cluster is driven by a continuous-deployment controller - <strong>Argo CD</strong>,
      <strong>Flux</strong>, <strong>Rancher Fleet</strong>, <strong>Crossplane</strong> - there is
      one interaction with plug worth knowing about. It affects
      <code>-s</code> only (publishing a name <em>into</em> the cluster); consuming cluster services
      is untouched, and everything here applies to Kubernetes and Swarm alike.
    </p>

    <h3>Why the two disagree</h3>
    <p>
      A CD controller's whole job is to keep the cluster equal to what a repository declares. It
      watches the live objects, and whatever differs from the declared state is
      <em>drift</em> - something to undo. That is the feature you deployed it for.
    </p>
    <p>
      plug's <code>-s</code> does the opposite on purpose: for the lifetime of a session it
      <strong>modifies a live object</strong> so a cluster name points at your machine instead of
      the deployed workload. On Kubernetes that means patching the Service's
      <code>spec.selector</code> and <code>spec.ports</code>; on Swarm, the DNS alias. The
      controller sees exactly what it is built to revert, and reverts it - usually within minutes.
    </p>
    <p>
      Nothing errors on either side. plug's patch is valid and applies cleanly; the controller's
      restore is valid too. The name simply stops pointing at you, quietly, some time after the
      session started.
    </p>

    <h3>How it shows up</h3>
    <p>
      plug proves the path end to end in the background and warns when the name never carried
      traffic. Which warning you get depends on whether the deployed workload is running:
    </p>
    <table>
      <thead>
        <tr><th scope="col">Warning</th><th scope="col">What happened</th></tr>
      </thead>
      <tbody>
        <tr>
          <td><code>is not reachable inside the cluster</code></td>
          <td>The workload is <strong>not</strong> deployed. The restore left the Service selecting
            a workload that does not exist - no endpoints, and connections time out.</td>
        </tr>
        <tr>
          <td><code>is answered by something else</code></td>
          <td>The workload <strong>is</strong> running. The name went back to it, so requests are
            served by the deployed version and your local process never sees one.</td>
        </tr>
      </tbody>
    </table>
    <p>
      The second is the one to watch for: nothing looks broken. The application answers, the page
      loads - it is simply not your code answering. If a session seems to work for a few minutes
      and then stops reflecting your changes, this is the first thing to check.
    </p>

    <h3>Confirming it</h3>
    <p>
      Look at the annotations and labels on the object plug tried to take over
      (<code>kubectl describe svc &lt;name&gt;</code>, or <code>d</code> on the Service in k9s):
    </p>
    <table>
      <thead>
        <tr><th scope="col">Controller</th><th scope="col">What to look for</th></tr>
      </thead>
      <tbody>
        <tr><td>Argo CD</td><td><code>argocd.argoproj.io/tracking-id</code> (annotation) or
          <code>app.kubernetes.io/instance</code> (label)</td></tr>
        <tr><td>Flux</td><td><code>kustomize.toolkit.fluxcd.io/name</code>,
          <code>helm.toolkit.fluxcd.io/name</code></td></tr>
        <tr><td>Rancher Fleet</td><td><code>fleet.cattle.io/bundle-name</code></td></tr>
        <tr><td>Crossplane, operators</td><td><code>crossplane.io/composite</code>, or an
          <code>ownerReferences</code> entry with <code>controller: true</code></td></tr>
      </tbody>
    </table>
    <div class="callout">
      <strong>Two false leads.</strong> <code>app.kubernetes.io/managed-by: Helm</code> on its own
      means nothing here - Helm acts on an upgrade and does not reconcile, so a chart-deployed
      Service is perfectly takeable. And <code>app.kubernetes.io/instance</code> is a standard
      Kubernetes label that Helm sets on everything; it only indicates Argo CD when Argo is
      configured to track by label, so the annotation is the reliable signal.
    </div>

    <h3>Argo CD</h3>
    <p>
      Tell the Application not to compare the two fields plug changes. Everything else about the
      Service keeps being reconciled:
    </p>
    <app-code lang="yaml">spec:
  ignoreDifferences:
    - group: ""
      kind: Service
      name: my-service
      namespace: my-namespace
      jsonPointers:
        - /spec/selector
        - /spec/ports</app-code>
    <p>
      This lives in the <code>Application</code> object (a CRD, usually in the <code>argocd</code>
      namespace) - <strong>not in the chart</strong>. On a cluster where plug is used routinely,
      set it once for every Service in the <code>argocd-cm</code> ConfigMap instead of Application
      by Application. The empty prefix in the key is how the core API group is spelled:
    </p>
    <app-code lang="yaml">apiVersion: v1
kind: ConfigMap
metadata:
  name: argocd-cm
  namespace: argocd
data:
  resource.customizations.ignoreDifferences._Service: |
    jsonPointers:
      - /spec/selector
      - /spec/ports</app-code>
    <p>
      For a one-off, turning off <strong>Self Heal</strong> (<em>App Details → Sync Policy</em>)
      also works and needs no manifest change - but it suspends drift correction for the whole
      Application, it is easy to forget to turn back on, and a Git push or a manual sync re-applies
      the Service anyway.
    </p>

    <h3>Flux</h3>
    <p>
      Flux is the one controller that takes the instruction <strong>on the resource itself</strong>,
      so here the chart <em>is</em> the right place:
    </p>
    <app-code lang="yaml">metadata:
  annotations:
    kustomize.toolkit.fluxcd.io/reconcile: disabled</app-code>
    <p>
      Note this is broader than Argo's <code>ignoreDifferences</code>: it stops reconciling the
      whole object, not two fields. The same annotation can be applied through the
      <code>Kustomization</code> with <code>spec.patches</code> if you would rather not carry it in
      the chart.
    </p>

    <h3>Rancher Fleet</h3>
    <p>
      Fleet compares with patches declared in the bundle's <code>fleet.yaml</code>:
    </p>
    <app-code lang="yaml">diff:
  comparePatches:
    - apiVersion: v1
      kind: Service
      name: my-service
      namespace: my-namespace
      operations:
        - op: remove
          path: /spec/selector
        - op: remove
          path: /spec/ports</app-code>

    <h3>Crossplane and bespoke operators</h3>
    <p>
      When a Service is managed as a Crossplane <code>Object</code>, switching that resource to
      <code>managementPolicies: ["Observe"]</code> stops it being re-applied. An operator written
      in-house rarely offers an equivalent: there, the options are to suspend the operator for the
      duration, or to serve a name it does not own.
    </p>

    <h3>After a session</h3>
    <p>
      Ending a session properly (<code>Ctrl-C</code>) is what restores the object, from a receipt
      plug stored on it. That matters more than usual here, because the extra selector key plug
      adds is <strong>not</strong> removed by the controller - it only manages fields it knows
      about. A session killed with <code>kill -9</code> can therefore leave a Service selecting
      both its own workload and plug, which matches no pod at all: the name then answers nobody,
      with or without plug running. If that happens, remove the leftover <code>app: plug</code>
      key from the selector by hand.
    </p>

    <p>
      Details of what <code>-s</code> does per orchestrator are on the
      <a routerLink="/kubernetes">Kubernetes</a> and <a routerLink="/swarm">Swarm</a> pages; other
      symptoms are covered under <a routerLink="/troubleshooting">Troubleshooting</a>.
    </p>
  `,
})
export class ContinuousDeploymentComponent {}
