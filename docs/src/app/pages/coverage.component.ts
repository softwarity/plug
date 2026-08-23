import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';

type St = 'ok' | 'warn' | 'no' | 'na';

interface Row {
  feat: string;
  os?: St[]; // [Linux, macOS, Windows] for OS sections
  st?: St; // single status for flat sections
  note?: string;
  sub?: boolean;
}
interface Section {
  title: string;
  flat?: boolean;
  rows: Row[];
}
interface Hole {
  sev: 'no' | 'warn';
  t: string;
  d: string;
}

@Component({
  selector: 'app-coverage',
  imports: [RouterLink],
  preserveWhitespaces: true,
  styles: [
    `
      .lead { max-width: 66ch; }
      .snap { font-family: 'Courier New', Consolas, monospace; font-size: 0.8rem; color: var(--text-muted); margin: 4px 0 0; }

      .legend { display: flex; flex-wrap: wrap; gap: 16px; margin: 18px 0 6px; font-size: 0.82rem; }
      .legend span { display: inline-flex; align-items: center; gap: 7px; color: var(--text-muted); }
      .dot { width: 15px; height: 15px; border-radius: 50%; display: inline-grid; place-items: center;
             font-size: 10px; font-weight: 700; color: #0d1117; }
      .d-ok { background: var(--cov-ok); } .d-warn { background: var(--cov-warn); }
      .d-no { background: var(--cov-no); } .d-na { background: var(--cov-na); color: var(--text-primary); }

      .holes { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 12px; margin: 12px 0 8px; }
      .hole { background: var(--bg-secondary); border: 1px solid var(--border-color); border-radius: 10px;
              padding: 14px 16px; position: relative; overflow: hidden; }
      .hole::before { content: ''; position: absolute; left: 0; top: 0; bottom: 0; width: 3px; background: var(--sev); }
      .hole .n { font-family: 'Courier New', Consolas, monospace; font-size: 0.7rem; color: var(--text-muted); }
      .hole .t { font-weight: 600; margin: 3px 0 6px; color: var(--text-primary); font-size: 0.92rem; }
      .hole .d { font-size: 0.83rem; color: var(--text-secondary); line-height: 1.45; }

      section.cov { margin-top: 30px; }
      .s-title { font-size: 0.82rem; letter-spacing: 0.12em; text-transform: uppercase; color: var(--accent-purple);
                 margin: 0 0 10px; display: flex; align-items: center; gap: 12px; font-weight: 600; }
      .s-title::after { content: ''; flex: 1; height: 1px; background: var(--border-color); }

      .scroll { overflow-x: auto; border: 1px solid var(--border-color); border-radius: 10px; }
      table.cov { border-collapse: collapse; width: 100%; min-width: 640px; margin: 0; font-size: 0.88rem; }
      table.cov th, table.cov td { padding: 9px 13px; border-bottom: 1px solid var(--border-color); text-align: left; vertical-align: middle; }
      table.cov tbody tr:last-child td { border-bottom: none; }
      table.cov thead th { background: var(--bg-secondary); font-size: 0.7rem; letter-spacing: 0.06em;
                           text-transform: uppercase; color: var(--text-muted); font-weight: 600; }
      table.cov th.c, table.cov td.c { text-align: center; width: 92px; }
      /* nowrap kept the short labels on one line, but one 71-character entry in
         Data path then set the width of its whole table and squeezed the notes
         into a vertical ribbon. Cap the label column and let only the long ones
         wrap; give the notes a floor so they always have room to read. */
      td.feat { font-family: 'Courier New', Consolas, monospace; font-size: 0.83rem; color: var(--text-primary);
                white-space: normal; max-width: 44ch; }
      td.feat.sub { color: var(--text-secondary); padding-left: 22px; position: relative; }
      td.feat.sub::before { content: '↳'; position: absolute; left: 8px; color: var(--text-muted); }
      td.note { color: var(--text-secondary); font-size: 0.83rem; white-space: normal; min-width: 22ch; }
      table.cov tbody tr:hover td { background: rgba(163, 113, 247, 0.05); }

      .cell { display: inline-grid; place-items: center; width: 26px; height: 26px; border-radius: 7px;
              font-family: 'Courier New', Consolas, monospace; font-weight: 700; font-size: 0.95rem; }
      .st-ok .cell { background: rgba(63, 185, 80, 0.14); color: var(--cov-ok); }
      .st-warn .cell { background: rgba(210, 153, 34, 0.16); color: var(--cov-warn); }
      .st-no .cell { background: rgba(248, 81, 73, 0.15); color: var(--cov-no); }
      .st-na .cell { background: rgba(139, 148, 158, 0.12); color: var(--cov-na); }

      :host { --cov-ok: #3fb950; --cov-warn: var(--accent-yellow); --cov-no: var(--accent-red); --cov-na: var(--text-muted); }
    `,
  ],
  template: `
    <h2>Coverage matrix</h2>
    <p class="lead">
      What works where, and where the holes are - features × OS. One process run
      <strong>as if it were inside the cluster</strong>, via a userspace TUN over an SSH tunnel.
    </p>
    <p class="snap">snapshot {{ snapshot }} · CI re-proves install → grid → multicluster → reverse path → takeover → crash-recovery on every push, on 3 OSes × 3 cluster families (compose, Swarm, Kubernetes); rows noted <em>bench</em> are runtime-proven locally, not yet in CI</p>

    <div class="legend">
      <span><i class="dot d-ok">✓</i> works (proven at runtime)</span>
      <span><i class="dot d-warn">!</i> partial - not yet e2e-validated</span>
      <span><i class="dot d-no">✕</i> not yet</span>
      <span><i class="dot d-na">-</i> n/a by design</span>
    </div>

    <h3>Biggest holes - priority order</h3>
    <div class="holes">
      @for (h of holes; track h.t; let i = $index) {
        <div class="hole" [style.--sev]="h.sev === 'no' ? 'var(--cov-no)' : 'var(--cov-warn)'">
          <div class="n">0{{ i + 1 }}</div>
          <div class="t">{{ h.t }}</div>
          <div class="d">{{ h.d }}</div>
        </div>
      }
    </div>

    @for (sec of sections; track sec.title) {
      <section class="cov">
        <h3 class="s-title">{{ sec.title }}</h3>
        <div class="scroll">
          <table class="cov">
            <thead>
              @if (sec.flat) {
                <tr><th>Item</th><th class="c">Support</th><th>Notes</th></tr>
              } @else {
                <tr>
                  <th>Feature</th>
                  @for (o of os; track o) { <th class="c">{{ o }}</th> }
                  <th>Notes</th>
                </tr>
              }
            </thead>
            <tbody>
              @for (r of sec.rows; track r.feat) {
                <tr>
                  <td class="feat" [class.sub]="r.sub" [innerHTML]="r.feat"></td>
                  @if (sec.flat) {
                    <td class="c st-{{ r.st }}"><span class="cell">{{ glyph(r.st!) }}</span></td>
                  } @else {
                    @for (s of r.os!; track $index) {
                      <td class="c st-{{ s }}"><span class="cell">{{ glyph(s) }}</span></td>
                    }
                  }
                  <td class="note" [innerHTML]="r.note || ''"></td>
                </tr>
              }
            </tbody>
          </table>
        </div>
      </section>
    }

    <div class="callout">
      <strong>How this matrix is proven.</strong> Every CI run installs plug FROM the cluster on
      all three OSes (the real one-liners and privilege grants), runs the 4-language ×
      8-protocol grid natively over a mesh, and asserts simultaneous multicluster, outage
      recovery, env passthrough, the reverse direction (a cluster workload - and an
      external caller through a published gateway - reaches a runner-served name) and
      launcher/core version compat. See
      <a routerLink="/how-it-works">How it works</a>
      and the <a routerLink="/roadmap">roadmap</a>.
    </div>
  `,
})
export class CoverageComponent {
  protected readonly snapshot = '2026-08-06';
  protected readonly os = ['Linux', 'macOS', 'Windows'];

  protected glyph(s: St): string {
    return { ok: '✓', warn: '!', no: '✕', na: '-' }[s];
  }

  protected readonly holes: Hole[] = [
    {
      sev: 'warn',
      t: 'Windows under a real corporate VPN client',
      d: 'What a VPN does to DNS is now proven in CI on all three OSes: the selftest fabricates an extra adapter carrying a resolver that knows a name nothing else knows, and asserts plug follows it - and follows it back down when the VPN goes away. What no CI runner can bring is a real corporate client: split-tunnel routing, Windows conditional-DNS (NRPT) rules pushed by policy, MTU, and clients that intercept DNS on a loopback address. Everything else on Windows is proven in CI, including self-heal.',
    },
    {
      sev: 'warn',
      t: 'Long-lived sessions & load',
      d: 'Every CI session lives seconds with one connection at a time. Hours-long sessions, high connection counts, big transfers and laptop sleep/wake are not yet exercised.',
    },
  ];

  protected readonly sections: Section[] = [
    {
      title: 'Install & privilege',
      rows: [
        { feat: 'Install from the cluster (one-liner)', os: ['ok', 'ok', 'ok'], note: '<code>install | sh</code> · <code>install-windows | bash -s -- &lt;host&gt;</code> (Git Bash)' },
        { feat: 'No sudo/admin to install', os: ['ok', 'ok', 'ok'], note: 'binaries + PATH; one UAC only to create the service' },
        { feat: 'One-time privilege grant at install', os: ['ok', 'ok', 'ok'], note: 'setcap · setuid helper · SCM SYSTEM service (validated on a real box)' },
        { feat: '<b>plug &lt;cmd&gt;</b> without sudo/admin', os: ['ok', 'ok', 'ok'], note: 'Windows: a non-elevated launcher starts the SYSTEM service via its ACL (proven, LIMITED token)' },
        { feat: 'Child runs as you (privilege drop)', os: ['ok', 'ok', 'na'], note: "caps don't cross exec · Credential drop · service is SYSTEM" },
        { feat: 'Pre-create profile at install', os: ['ok', 'ok', 'ok'], note: 'reads host/port off the live ssh' },
        { feat: 'Uninstall', os: ['ok', 'ok', 'ok'], note: 'unix + Windows (remove-service, wipe) covered' },
      ],
    },
    {
      title: 'Data path',
      rows: [
        { feat: 'Userspace TUN (IP-layer capture)', os: ['ok', 'ok', 'ok'], note: '/dev/net/tun · utun · WinTUN' },
        { feat: 'Cluster-name DNS (real apps)', os: ['ok', 'ok', 'ok'], note: 'private resolv.conf · scutil store · WinTUN search-suffix + NRPT' },
        { feat: 'Single-label name via <code>getaddrinfo</code>', os: ['ok', 'ok', 'ok'], note: 'the real app path; Windows needs the <code>.plug</code> search suffix to issue a DNS query' },
        { feat: 'Works under a corporate VPN', os: ['ok', 'ok', 'warn'], note: 'macOS proven w/ GlobalProtect; a real Windows corporate client is still unproven (split-tunnel, NRPT-by-policy)' },
        { feat: 'Follows the resolver when a VPN comes up, drops, or the network changes', os: ['ok', 'ok', 'ok'], note: 'the servers are not a startup fact - <b>in CI on all three OSes</b>: the selftest fabricates a VPN (an extra adapter carrying a resolver that knows a name nothing else knows) and asserts that name resolves <i>through plug</i>, then stops when the VPN goes away. Includes what a VPN does not cause: on macOS one network service serves every SSID, so changing Wi-Fi moves the resolvers without moving the service. <code>plug doctor</code> reports where lookups actually go' },
        { feat: 'Every runtime (Node/JVM/Py/Go/gRPC)', os: ['ok', 'ok', 'ok'], note: 'IP-level capture, socket never touched' },
        { feat: 'Native selftest (datapath proof)', os: ['ok', 'ok', 'ok'], note: 'green on all three in CI' },
        { feat: 'e2e protocol matrix (8 protos × 4 langs)', os: ['ok', 'ok', 'ok'], note: 'native over a Tailscale mesh, against all THREE cluster families - compose, Swarm and Kubernetes (kind) - the same by-name path on Linux, macOS and Windows' },
        { feat: 'Split-horizon (short→cluster, FQDN→direct)', os: ['ok', 'ok', 'ok'], note: 'decided by the shape of the name - no config' },
        { feat: 'Reverse: serve a local port to the cluster (<code>-s</code>)', os: ['ok', 'ok', 'ok'], note: 'SSH remote-forward - a cluster workload fetches the runner in CI, path self-verified at startup; re-arm after reconnect <b>in CI</b> (the resilience cell restarts the agent mid-session)' },
        { feat: 'Reverse: external caller → published gateway → runner (HTTP)', os: ['ok', 'ok', 'ok'], note: 'a POST to a PUBLISHED cluster gateway calls a <code>-s</code> name that lands on the runner\'s local sink; the correlation id AND the full request path round-trip back (root and a deep path) - the API-gateway use case, proven from outside the cluster' },
        { feat: '<code>-s</code> name provisioned dynamically (no redeploy)', os: ['ok', 'ok', 'ok'], note: 'CI serves a name declared nowhere, from a linux/mac/win client, on all three backends: docker-sock signpost container (Compose), Swarm-service signpost on a <b>non-attachable overlay</b> (Swarm), Service through the Services-only RBAC (k8s) - created &amp; torn down per session, swept on agent restart' },
        { feat: 'Takeover of a deployed name (default)', os: ['ok', 'ok', 'ok'], note: 'a deployed workload owning a <code>-s</code> name is parked for the session and restored on exit - <b>all three backends in CI</b>: containers stopped (Compose), Swarm service scaled to 0 &amp; back to its <b>original replica count</b> (the CI target runs 2), k8s Service repointed via annotation receipt (ClusterIP identical through park/restore). Another session\'s name stays refused. Boot-gc restore after an agent crash AND the re-park on reconnect are <b>in CI</b> (resilience cell)' },
        { feat: 'Self-heal (VPN / sleep / agent restart)', os: ['ok', 'ok', 'ok'], note: 'keepalive times out a zombie connection then reconnects &amp; re-provisions - <b>in CI on all three OSes</b>: the resilience cell restarts the agent mid-session and traffic re-parks in seconds (boot-gc restores, the reconnect re-arms)' },
      ],
    },
    {
      title: 'Daemon / persistence',
      rows: [
        { feat: 'Persistent global datapath', os: ['na', 'ok', 'ok'], note: 'Linux autonomous · macOS daemon · Windows SCM service (validated)' },
        { feat: 'Survives process restarts', os: ['ok', 'ok', 'ok'], note: 'per-launch · daemon · service + 30&thinsp;s self-teardown (validated)' },
        { feat: 'Graft (multi-process, same cluster)', os: ['ok', 'ok', 'ok'], note: 'flock leader/graft · service is the single owner (3 concurrent sessions proven)' },
        { feat: '<b>plug down</b>', os: ['na', 'ok', 'ok'], note: 'stops the daemon (macOS) / service (Windows)' },
      ],
    },
    {
      title: 'Multicluster - different clusters at once',
      rows: [
        { feat: 'Simultaneous different clusters', os: ['ok', 'ok', 'ok'], note: 'proven simultaneously in CI on all three - mount-ns · daemon · SYSTEM service' },
        { feat: 'PID-at-connect attribution', os: ['na', 'ok', 'ok'], note: '<code>multiDial</code> shared - proven in CI on macOS and Windows (2 live clusters, same name, right backend)' },
        { feat: 'ppidOf', os: ['ok', 'ok', 'ok'], note: '/proc · ps · ToolHelp (unit-tested)', sub: true },
        { feat: 'pidForLocalPort', os: ['ok', 'ok', 'ok'], note: '/proc/net · lsof · GetExtendedTcpTable (unit-tested)', sub: true },
        { feat: 'procStart (recycle guard)', os: ['ok', 'ok', 'ok'], note: 'ps lstart · /proc stat · GetProcessTimes - rejects a reused PID', sub: true },
        { feat: 'N-tunnel global daemon', os: ['na', 'ok', 'ok'], note: 'one tunnel per cluster in the daemon/service - proven simultaneously in CI (2 live clusters)' },
      ],
    },
    {
      title: 'Profiles & CLI',
      rows: [
        { feat: 'Profiles ~/.plug/*.conf', os: ['ok', 'ok', 'ok'], note: 'create by naming · ls · rm · rn/mv · test' },
        { feat: '-p profile / --host / --port', os: ['ok', 'ok', 'ok'], note: 'flags override the selected profile' },
        { feat: 'Launcher versions (per-cluster)', os: ['ok', 'ok', 'ok'], note: 'versions · <code>.exe</code> + wintun.dll handled on Windows' },
        { feat: 'Host-key TOFU pin', os: ['ok', 'ok', 'ok'], note: 'localhost skipped; pin chowned (macOS)' },
      ],
    },
    {
      title: 'Agent deployment',
      flat: true,
      rows: [
        { feat: 'Docker Compose / Swarm', st: 'ok', note: 'agent image joins the stack network - both in CI (Swarm: a real single-node swarm, the agent as a Swarm service on a non-attachable overlay)' },
        { feat: 'Kubernetes - NodePort', st: 'ok', note: '<code>deploy/plug-k8s.yaml</code> applied as published (kind) - the CI legs install and run through it on every push' },
        { feat: 'Kubernetes - kubectl port-forward', st: 'ok', note: 'zero exposed port, API-server RBAC - the k8s cluster job answers the agent contract through a live port-forward on every push' },
        { feat: 'Cross-namespace', st: 'ok', note: 'via FQDN <code>svc.othernamespace</code>' },
      ],
    },
    {
      title: 'Design limits (all OS)',
      flat: true,
      rows: [
        { feat: 'UDP / QUIC / ping', st: 'no', note: 'TCP only (SSH tunnel); most clients fall back to TCP' },
        { feat: 'IPv6 literal (hard-coded)', st: 'no', note: 'fake IPs are IPv4; by-name cluster service is fine' },
      ],
    },
  ];
}
