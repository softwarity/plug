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
      td.feat { font-family: 'Courier New', Consolas, monospace; font-size: 0.83rem; color: var(--text-primary); white-space: nowrap; }
      td.feat.sub { color: var(--text-secondary); padding-left: 22px; position: relative; }
      td.feat.sub::before { content: '↳'; position: absolute; left: 8px; color: var(--text-muted); }
      td.note { color: var(--text-secondary); font-size: 0.83rem; white-space: normal; }
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
      What works where, and where the holes are — features × OS. One process run
      <strong>as if it were inside the cluster</strong>, via a userspace TUN over an SSH tunnel.
    </p>
    <p class="snap">snapshot {{ snapshot }} · Windows validated end-to-end on a real box: no-admin service + multicluster</p>

    <div class="legend">
      <span><i class="dot d-ok">✓</i> works (proven at runtime)</span>
      <span><i class="dot d-warn">!</i> partial — not yet e2e-validated</span>
      <span><i class="dot d-no">✕</i> not yet</span>
      <span><i class="dot d-na">–</i> n/a by design</span>
    </div>

    <h3>Biggest holes — priority order</h3>
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
      <strong>Read this alongside the code.</strong> The Windows datapath (WinTUN + name resolution)
      is proven on a real machine; the SYSTEM service that removes the per-run admin and enables
      multicluster is written and build-validated on all three OSes but not yet runtime-validated —
      see <a href="https://github.com/softwarity/plug/blob/main/docs/windows-service.md" target="_blank" rel="noopener">docs/windows-service.md</a>
      and the <a routerLink="/roadmap">roadmap</a>.
    </div>
  `,
})
export class CoverageComponent {
  protected readonly snapshot = '2026-07-11';
  protected readonly os = ['Linux', 'macOS', 'Windows'];

  protected glyph(s: St): string {
    return { ok: '✓', warn: '!', no: '✕', na: '–' }[s];
  }

  protected readonly holes: Hole[] = [
    {
      sev: 'warn',
      t: 'Windows e2e in CI',
      d: 'The full Windows path — Git Bash install, no-admin service, multicluster, name resolution — is validated by hand on a real box; automate it on a self-hosted (WSL2) or mesh-connected runner.',
    },
    {
      sev: 'warn',
      t: 'Windows under a corporate VPN + self-heal',
      d: 'A plain cluster is proven end-to-end on Windows (datapath, DNS, no-admin, multicluster). Corporate-VPN behaviour and VPN/sleep self-heal there are still unproven.',
    },
    {
      sev: 'warn',
      t: 'First run after a cold start / host-key reset',
      d: 'The very first plug after the service has torn down (or a known_hosts reset) can exceed the 12 s ready-wait while the tunnel comes up; the next run is instant. A more patient wait would smooth it.',
    },
    {
      sev: 'warn',
      t: 'self-update on Linux',
      d: 'Loses file-caps on update (pre-existing). macOS already re-applies its setuid bit.',
    },
  ];

  protected readonly sections: Section[] = [
    {
      title: 'Install & privilege',
      rows: [
        { feat: 'Install from the cluster (one-liner)', os: ['ok', 'ok', 'ok'], note: '<code>install | sh</code> · <code>install-windows | bash</code> (Git Bash, <code>PLUG_HOST=</code>)' },
        { feat: 'No sudo/admin to install', os: ['ok', 'ok', 'ok'], note: 'binaries + PATH; one UAC only to create the service' },
        { feat: 'One-time privilege grant at install', os: ['ok', 'ok', 'ok'], note: 'setcap · setuid helper · SCM SYSTEM service (validated on a real box)' },
        { feat: '<b>plug &lt;cmd&gt;</b> without sudo/admin', os: ['ok', 'ok', 'ok'], note: 'Windows: a non-elevated launcher starts the SYSTEM service via its ACL (proven, LIMITED token)' },
        { feat: 'Child runs as you (privilege drop)', os: ['ok', 'ok', 'na'], note: "caps don't cross exec · Credential drop · service is SYSTEM" },
        { feat: 'self-update preserves privilege', os: ['warn', 'ok', 'na'], note: 'setcap lost on update · macOS re-applies setuid' },
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
        { feat: 'Works under a corporate VPN', os: ['ok', 'ok', 'warn'], note: 'macOS proven w/ GlobalProtect; Windows unproven' },
        { feat: 'Every runtime (Node/JVM/Py/Go/gRPC)', os: ['ok', 'ok', 'ok'], note: 'IP-level capture, socket never touched' },
        { feat: 'Native selftest (datapath proof)', os: ['ok', 'ok', 'ok'], note: 'green on all three in CI' },
        { feat: 'e2e protocol matrix (8 protos × 4 langs)', os: ['ok', 'na', 'na'], note: 'Docker→Linux; mac/win via native selftest (Windows e2e planned)' },
        { feat: 'Split-horizon (short→cluster, FQDN→direct)', os: ['ok', 'ok', 'ok'], note: '+ <code>PLUG_DIRECT</code> overrides' },
        { feat: 'Self-heal (VPN / sleep / agent restart)', os: ['ok', 'ok', 'warn'], note: 'keepalive+reconnect; Windows unproven' },
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
      title: 'Multicluster — different clusters at once',
      rows: [
        { feat: 'Simultaneous different clusters', os: ['ok', 'ok', 'ok'], note: 'Linux mount-ns · <b>macOS + Windows validated e2e</b> (neo + llm, by name)' },
        { feat: 'PID-at-connect attribution', os: ['na', 'ok', 'ok'], note: '<code>multiDial</code> shared; proven on 2 live clusters on macOS &amp; Windows' },
        { feat: 'ppidOf', os: ['ok', 'ok', 'ok'], note: '/proc · ps · ToolHelp (unit-tested)', sub: true },
        { feat: 'pidForLocalPort', os: ['ok', 'ok', 'ok'], note: '/proc/net · lsof · GetExtendedTcpTable (unit-tested)', sub: true },
        { feat: 'procStart (recycle guard)', os: ['ok', 'ok', 'ok'], note: 'ps lstart · /proc stat · GetProcessTimes — rejects a reused PID', sub: true },
        { feat: 'N-tunnel global daemon', os: ['na', 'ok', 'ok'], note: 'macOS daemon · Windows SCM service — both validated' },
      ],
    },
    {
      title: 'Profiles & CLI',
      rows: [
        { feat: 'Profiles ~/.plug/*.conf', os: ['ok', 'ok', 'ok'], note: 'create by naming · ls · rm · rn/mv · test' },
        { feat: '-p profile / --host / --port / env', os: ['ok', 'ok', 'ok'], note: 'env = <code>$PLUG_HOST $PLUG_PORT</code>' },
        { feat: 'Launcher versions (per-cluster)', os: ['ok', 'ok', 'ok'], note: 'versions · self-update · <code>.exe</code> + wintun.dll handled on Windows' },
        { feat: 'Host-key TOFU pin', os: ['ok', 'ok', 'ok'], note: 'localhost skipped; pin chowned (macOS)' },
        { feat: 'Port-forward escape hatch (forward=)', os: ['ok', 'ok', 'ok'], note: 'rewrites an env var to a local port' },
      ],
    },
    {
      title: 'Agent deployment',
      flat: true,
      rows: [
        { feat: 'Docker Compose / Swarm', st: 'ok', note: 'agent image joins the stack network' },
        { feat: 'Kubernetes — NodePort', st: 'ok', note: '<code>deploy/plug-k8s.yaml</code>, --port 32222' },
        { feat: 'Kubernetes — kubectl port-forward', st: 'ok', note: 'zero exposed port, API-server RBAC' },
        { feat: 'Cross-namespace', st: 'ok', note: 'via FQDN <code>svc.othernamespace</code>' },
        { feat: 'kubectl exec transport', st: 'no', note: 'dropped — port-forward already covers it' },
      ],
    },
    {
      title: 'Design limits (all OS)',
      flat: true,
      rows: [
        { feat: 'UDP / QUIC / ping', st: 'no', note: 'TCP only (SSH tunnel); most clients fall back to TCP' },
        { feat: 'IPv6 literal (hard-coded)', st: 'no', note: 'fake IPs are IPv4; by-name cluster service is fine' },
        { feat: 'Root / helper required', st: 'na', note: 'by design — the price of covering every runtime' },
        { feat: 'Authentication', st: 'na', note: 'none by design — trusted dev clusters only' },
      ],
    },
  ];
}
