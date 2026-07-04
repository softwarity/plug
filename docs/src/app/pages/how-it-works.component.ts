import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-how-it-works',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>How it works</h2>

    <p>
      plug is a thin, opinionated orchestration of proven pieces: an SSH tunnel to a tiny agent in
      the cluster, a local <strong>SOCKS5 proxy</strong> on top of it, and the child command's
      environment pointed at that proxy. No root, no TUN, no daemon.
    </p>

    <app-code lang="text">┌─ your laptop ──────────────────┐        ┌─ swarm cluster ────────────┐
│  plug &lt;cmd&gt;                    │        │  plug-agent (alpine+sshd)  │
│   ├─ local SOCKS5 proxy ───────┼──ssh───┼─→ direct-tcpip: sshd dials │
│   │   ALL_PROXY → child        │  :2222 │   service:port & resolves  │
│   ├─ per-session port-forwards │        │   names inside the cluster │
│   └─ runs &lt;cmd&gt;                │        │                            │
└────────────────────────────────┘        └────────────────────────────┘</app-code>

    <h3>The stages</h3>
    <ul>
      <li>
        <strong>SSH transport.</strong> plug connects to the agent's <code>sshd</code> (embedded
        key, in-process — no <code>ssh</code> binary needed). Every outbound flow becomes an SSH
        <code>direct-tcpip</code> channel: <code>sshd</code> opens the real connection to
        <code>service:port</code> from <em>inside</em> the cluster, so it resolves the name with the
        cluster's own resolver. No server code of ours — stock <code>sshd</code>.
      </li>
      <li>
        <strong>Local SOCKS5 proxy.</strong> plug runs a SOCKS5 proxy bound to
        <code>127.0.0.1</code> and exports <code>ALL_PROXY=socks5h://…</code> (plus
        <code>JAVA_TOOL_OPTIONS=-DsocksProxyHost…</code>) into the child. The <code>h</code> in
        <code>socks5h</code> means the <em>hostname</em> is sent to the proxy — so cluster names are
        resolved cluster-side, with nothing touched on your machine.
      </li>
      <li>
        <strong>Port-forwards (optional).</strong> Raw-TCP drivers that ignore the proxy get a
        per-session local port to their cluster service, and plug injects its address into the
        child's environment — see <a routerLink="/profiles">Profiles</a>.
      </li>
      <li>
        <strong>Your command runs, then teardown.</strong> stdio is passed through (pipes, colors,
        <kbd>Ctrl-C</kbd> behave normally). When it exits, the proxy and forwards close with it.
        Nothing global was ever changed, so nothing can leak.
      </li>
    </ul>

    <h3>Why this design</h3>
    <ul>
      <li><strong>No root, no daemon, no TUN.</strong> Everything is a userspace proxy + env vars. Install is a single static binary.</li>
      <li><strong>Multi-cluster by nature.</strong> Nothing global is touched (no system DNS, no <code>/etc/hosts</code>, no firewall), so the same process can run against several clusters at once — each session has its own proxy and forward ports.</li>
      <li><strong>The trade-off.</strong> A SOCKS proxy is not transparent: a library must honor the proxy env. HTTP clients and the whole JVM do; some raw-TCP drivers (Node's <code>amqplib</code>) don't — those use a <a routerLink="/profiles">port-forward</a>.</li>
    </ul>

    <h3>Built with open source</h3>
    <p>plug stands on the shoulders of these projects — thank you:</p>
    <table>
      <thead>
        <tr><th>Dependency</th><th>Role</th><th>License</th></tr>
      </thead>
      <tbody>
        <tr><td><a href="https://www.openssh.com/" target="_blank" rel="noopener">OpenSSH</a></td><td>The transport: client (<code>golang.org/x/crypto/ssh</code>, in-process) and <code>sshd</code> in the agent doing the <code>direct-tcpip</code> dials</td><td>BSD</td></tr>
        <tr><td><a href="https://go.dev/" target="_blank" rel="noopener">Go</a></td><td>The CLI — one static binary per platform, no runtime dependencies (~6&nbsp;MB)</td><td>BSD</td></tr>
        <tr><td><a href="https://www.alpinelinux.org/" target="_blank" rel="noopener">Alpine Linux</a></td><td>Base of the agent image — just <code>sshd</code> + the served binaries</td><td>MIT</td></tr>
      </tbody>
    </table>

    <div class="callout">
      <strong>Why not mirrord / Telepresence?</strong> Both are excellent — for Kubernetes. plug
      exists because nothing equivalent existed for <strong>Docker Swarm</strong>, and its agent is
      simple enough (a stock <code>sshd</code>) to embed later into another host — like an API
      gateway (see <a routerLink="/roadmap">Roadmap</a>). Full syscall-level transparency across any
      driver, with zero config, is exactly mirrord's domain (library injection) — plug trades that
      for zero root and native multi-cluster.
    </div>
  `,
})
export class HowItWorksComponent {}
