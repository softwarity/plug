# Deploying the plug agent

The agent is a stock **openssh** container. The `plug` CLI captures a local
process's cluster traffic at the IP layer (userspace TUN + gVisor netstack) and
splices each flow — by name — through a single SSH tunnel to this agent. sshd
then resolves the name and opens the connection **from inside the cluster** via
an SSH `direct-tcpip` channel. No SOCKS proxy, no server-side code of ours.

> **Trusted dev clusters only.** The SSH keypair is embedded in the repo and in
> every binary — a transport detail, not a secret. Anyone who can reach the
> agent port gets full network access to the cluster. There is **no
> authentication by design**. Never expose the agent on an untrusted network.

## Docker / Swarm

Add the service to your application stack so it joins the stack network
automatically:

```yaml
services:
  plug:
    image: docker.io/softwarity/plug:latest
    ports:
      - "2222:22"
```

Or run one standalone agent across several stacks — see
[`plug-stack.yml`](plug-stack.yml).

## Kubernetes

```bash
kubectl -n <your-namespace> apply -f plug-k8s.yaml
```

Deploy it in the **same namespace** as the services you want to reach: sshd
resolves short names (`myservice`) via the pod's resolver (CoreDNS) from inside
that namespace. See [`plug-k8s.yaml`](plug-k8s.yaml) — a Deployment plus a
NodePort Service (`32222` → container `22`), with a TCP readiness probe and a
modest resource footprint.

### Two ways to connect

- **NodePort** — reachable on any node:

  ```bash
  plug --host <a-node> --port 32222 <cmd>
  ```

- **`kubectl port-forward`** — nothing exposed on the cluster; the tunnel rides
  the API server and is gated by its RBAC:

  ```bash
  kubectl -n <ns> port-forward svc/plug 2222:2222
  plug --host localhost <cmd>
  ```

### Cross-namespace

Short names only resolve within the agent's own namespace. To reach a service
elsewhere, use its FQDN in your app's connection string:

```
myservice.othernamespace
myservice.othernamespace.svc.cluster.local
```
