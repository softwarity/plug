import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';

@Component({
  selector: 'app-roadmap',
  imports: [RouterLink],
  template: `
    <h2>Roadmap</h2>
    <p>
      What is coming, and what is left out on purpose. What plug does today is in the rest of this
      documentation.
    </p>

    <h3>Coming</h3>

    <h4>UDP by name</h4>
    <p>
      A cluster service reached over UDP, by its name, the way TCP is today: a DNS server of the
      cluster, StatsD, syslog, any request and response protocol. Until then, UDP to the cluster is
      not carried, and plug says so in its log rather than letting it look like a hang. Most
      clients of QUIC and HTTP/3 already fall back to TCP. Real-time media is not the aim: the
      datagrams will ride the tunnel in order, which suits requests and answers, not a video call.
    </p>

    <h4>IPv6 addresses written in the code</h4>
    <p>
      A service reached by its name already works, whatever its address. An IPv6 address written
      as is in a program or a config will be carried too.
    </p>

    <h4>Opening a session from an AI agent</h4>
    <p>
      <a routerLink="/mcp"><code>plug mcp</code></a> tells an AI coding agent what plug knows. Opening
      and closing a session from it is still being designed: a session is a process that lives
      while the agent works, and it deserves its own shape.
    </p>

    <h3>Left out on purpose</h3>

    <h4>A Kubernetes-only way in</h4>
    <p>
      plug behaves the same on Docker, Compose, Swarm, Kubernetes and OpenShift, and stays that
      way: a feature that exists on one of them only is a second way of working to learn. On
      Kubernetes, <code>kubectl port-forward</code> already reaches the agent with no exposed port,
      each developer's kubeconfig gating it (<a routerLink="/security">security model</a>).
    </p>

    <h4>Homebrew, apt and other package channels</h4>
    <p>
      plug installs from the cluster, and runs the version each cluster serves. A package channel
      would be a second source, with versions that drift from the clusters'.
    </p>

    <h4>Keystrokes in a running session</h4>
    <p>
      The command plug runs owns the keyboard: Vite reads <code>r</code>, <code>u</code> and
      <code>q</code>, nodemon reads <code>rs</code>, a REPL reads everything. Any key plug kept for
      itself would be taken from that program. Another terminal is where to ask instead:
      <code>plug mounts</code> lists what the live sessions have mounted, <code>plug doctor</code>
      checks what they rely on.
    </p>
  `,
})
export class RoadmapComponent {}
