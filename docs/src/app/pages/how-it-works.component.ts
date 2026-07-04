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
      the cluster, three interception layers on top of it (an injected
      <strong>connect()/DNS hook</strong>, an <strong>HTTP proxy</strong>, and a
      <strong>SOCKS5 proxy</strong>), and the child command wired to all three. No root, no TUN,
      no daemon.
    </p>

    <app-code lang="text">┌─ your laptop ──────────────────┐        ┌─ swarm cluster ────────────┐
│  plug &lt;cmd&gt;                    │        │  plug-agent (alpine+sshd)  │
│   ├─ connect()/DNS hook (inj.) │        │                            │
│   ├─ HTTP proxy   ─────────────┼──ssh───┼─→ direct-tcpip: sshd dials │
│   ├─ SOCKS5 proxy              │  :2222 │   service:port & resolves  │
│   └─ runs &lt;cmd&gt; → all three    │        │   names inside the cluster │
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
        <strong>Transparent connect()/DNS hook.</strong> A tiny native library is injected into the
        child (<code>DYLD_INSERT_LIBRARIES</code> on macOS, <code>LD_PRELOAD</code> on Linux); it
        interposes <code>connect()</code> and <code>getaddrinfo()</code>. So <em>every</em> outbound
        TCP connection and name lookup of a <strong>libc-based</strong> process (Node, the JVM,
        Python, curl…) is routed through the tunnel, resolved cluster-side — which is what makes
        raw-TCP drivers (<code>amqplib</code>, <code>pg</code>, <code>mongodb</code>,
        <code>redis</code>, gRPC…) work with no per-service config.
      </li>
      <li>
        <strong>HTTP proxy + SOCKS5 proxy.</strong> Alongside the hook, plug exports
        <code>HTTP_PROXY</code>/<code>HTTPS_PROXY</code> (a local HTTP proxy) and
        <code>ALL_PROXY=socks5h://…</code> + <code>JAVA_TOOL_OPTIONS=-DsocksProxyHost…</code> (a
        local SOCKS5 proxy). These catch proxy-aware clients even where injection doesn't apply —
        and the JVM routes <em>all</em> its sockets through <code>-DsocksProxyHost</code>. The
        <code>h</code> in <code>socks5h</code> means the hostname is resolved cluster-side.
      </li>
      <li>
        <strong>Port-forwards (fallback).</strong> For what the hook can't reach —
        <strong>Go</strong>/statically-linked binaries (they bypass libc), non-TCP — declare a
        per-session local port to the cluster service; plug injects its address into the child's
        environment. See <a routerLink="/profiles">Profiles</a>.
      </li>
      <li>
        <strong>Your command runs, then teardown.</strong> stdio is passed through (pipes, colors,
        <kbd>Ctrl-C</kbd> behave normally). When it exits, the proxy and forwards close with it.
        Nothing global was ever changed, so nothing can leak.
      </li>
    </ul>

    <h3>Why this design</h3>
    <ul>
      <li><strong>No root, no daemon, no TUN.</strong> Userspace proxies + an injected userspace library + env vars. Install is a single static binary.</li>
      <li><strong>Multi-cluster by nature.</strong> Nothing global is touched (no system DNS, no <code>/etc/hosts</code>, no firewall), so the same process can run against several clusters at once — each session has its own proxies, hook and forward ports.</li>
      <li><strong>Honest limits.</strong> The hook is libc-only: <strong>Go</strong>/statically-linked binaries issue the <code>connect</code> syscall directly and bypass it; non-TCP (UDP, QUIC) is untouched; on macOS, SIP-protected system binaries (<code>/usr/bin/*</code>) strip the injection. A <a routerLink="/profiles">port-forward</a> is the fallback for those. Native Windows isn't covered yet — use WSL2.</li>
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
