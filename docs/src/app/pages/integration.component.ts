import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

const host = `import (
	"golang.org/x/crypto/ssh"

	"github.com/softwarity/plug/agent"
)

type host struct{ /* your vault, your key store, your audit */ }

// The server's identity, kept where the gateway keeps its secrets.
func (h *host) HostKey() (ssh.Signer, error) { return h.vault.SSHHostKey() }

// Which keys get in, and under whose name. Called on every connection.
func (h *host) Verify(key ssh.PublicKey) (who string, ok bool) {
	dev, ok := h.keys.Lookup(ssh.FingerprintSHA256(key))
	return dev.Name, ok
}

// What is served right now, and by whom. Information, never authorisation.
func (h *host) Served(e agent.NameEvent) { h.audit.Served(e) }
func (h *host) Unserved(name string)     { h.audit.Unserved(name) }

// Optional (agent.SessionHost): every plug run, as one session.
func (h *host) SessionOpened(e agent.SessionEvent) { h.audit.Opened(e) }
func (h *host) SessionClosed(e agent.SessionEvent) { h.audit.Closed(e) }`;

const start = `const verbArg = "__plug-verb"

func main() {
	// A verb is a process of its own: the agent runs this binary again with
	// the request in SSH_ORIGINAL_COMMAND, and agent.Main answers it.
	if len(os.Args) > 1 && os.Args[1] == verbArg {
		agent.Main()
		return
	}
	// ... the gateway starts ...
	self, _ := os.Executable()
	version := readVersion("/opt/plug/VERSION") // what the download account hands out
	ctl := agent.NewControl()
	go func() {
		err := agent.Start(ctx, agent.Config{
			Host:           &host{ /* ... */ },
			Control:        ctl,
			Addr:           ":2222",
			Version:        version,
			VerbCommand:    []string{self, verbArg},
			SignpostImage:  "softwarity/plug:" + version,
			NoSelfUpdate:   true,
			KeyInstruction: "register the key it prints in your profile on this gateway",
		})
		slog.Error("plug agent stopped", "err", err)
	}()
}`;

const dockerfile = `# What the download account hands out: the hosted client, its installers, its version.
FROM softwarity/plug:<version>-hosted AS plug

FROM your-gateway-base
COPY --from=plug /opt/plug /opt/plug`;

const pin = `v=<version>
commit=$(git ls-remote https://github.com/softwarity/plug.git "refs/tags/v$v^{}" "refs/tags/v$v" | head -1 | cut -f1)
go get "github.com/softwarity/plug/agent@$commit"`;

const podIP = `env:
  - name: PLUG_POD_IP
    valueFrom:
      fieldRef:
        fieldPath: status.podIP`;

const control = `// The page that lists live sessions opens on this, then follows the events.
open := ctl.Sessions()

// An administrator ends one run. The developer reads the reason.
ctl.CloseSession(id, "closed by an administrator of this gateway")

// A key withdrawn, or its owner's right: everything it has open ends now.
ctl.CloseKey(fingerprint, "your key was removed from this gateway")`;

@Component({
  selector: 'app-integration',
  imports: [CodeComponent, RouterLink],
  styles: [
    `
      h3 {
        margin-top: 36px;
      }
      .check li {
        margin: 6px 0;
      }
    `,
  ],
  template: `
    <h2>Integration</h2>

    <p>
      A gateway can run the plug agent <strong>inside its own binary</strong> instead of beside it.
      The agent is a Go package, <code>github.com/softwarity/plug/agent</code>: the SSH server, the
      verbs, the names, the parking, all of it compiled into the gateway. The developers keep the
      same CLI and the same commands; what changes is who answers four questions the agent cannot
      answer alone:
    </p>
    <ul>
      <li><strong>The server's identity</strong>, kept in the gateway's vault rather than in a file a recreated container loses.</li>
      <li><strong>Which keys get in, and under whose name</strong>, from the gateway's own users.</li>
      <li><strong>What is served right now, and by whom</strong>, so a page can say "checkout, by Alice".</li>
      <li><strong>Which plug runs are open</strong>, and the hand to end one.</li>
    </ul>
    <p>
      <a routerLink="/meerkat">Meerkat</a> is built this way. This page is what any gateway needs to
      do the same.
    </p>

    <h3>1. Answer the agent: a Host</h3>
    <p>
      <code>agent.Host</code> answers the first three: <code>HostKey</code>, <code>Verify</code>,
      and the pair <code>Served</code> / <code>Unserved</code>. A Host that also implements
      <code>agent.SessionHost</code> hears every plug run open and close.
    </p>
    <app-code lang="go">{{ host }}</app-code>
    <ul>
      <li>
        <code>Verify</code> is your admission: called on every connection, so it must be cheap or
        cached. An empty <code>who</code> means the key names software, not a person.
      </li>
      <li>
        The events are <strong>information, never authorisation</strong>. They arrive in order on
        one queue: a session's <code>SessionOpened</code> before its <code>Served</code>, the
        <code>Unserved</code> of what it still held before its <code>SessionClosed</code>. A slow or
        failing Host does not stop a developer, and a panic in it is absorbed and logged.
      </li>
      <li>
        <code>Unserved</code> is not optional: without it, a page keeps showing names nobody serves
        any more.
      </li>
    </ul>

    <h3>2. Start it</h3>
    <p>
      <code>agent.Start</code> serves until its context is cancelled, and returns errors rather than
      exiting. The verbs, on the other hand, exit on error, which is right for a process whose
      whole job is one answer and wrong inside a gateway: they run as a subprocess, your binary run
      again with a hidden argument.
    </p>
    <app-code lang="go">{{ start }}</app-code>
    <ul>
      <li>
        <code>Version</code> must be the version the download account answers: the CLI fetches the
        core for that version and checks its digest. Read it from <code>/opt/plug/VERSION</code>
        (step 3) and the two cannot drift.
      </li>
      <li>
        <code>SignpostImage</code> on Compose and Swarm: the container that carries a served name
        runs plug's image, not the gateway's, which has no agent binary in it. Kubernetes creates a
        Service and never uses it.
      </li>
      <li>
        <code>NoSelfUpdate</code>: the update verb rewrites the image of the deployment the agent
        runs in, which is the gateway's. Updating the gateway is the gateway's business.
      </li>
      <li>
        <code>KeyInstruction</code> ends an install, after "and": where the key
        <code>plug keygen</code> just made goes, in your words. One line, no quote or backslash,
        200 characters at most.
      </li>
      <li>
        <code>RequireOrchestrator</code> stays false: a missing Docker socket or role is logged and
        the gateway keeps running. Reaching the cluster (<code>-c</code>) still works; serving a
        name (<code>-s</code>) says what access is missing.
      </li>
      <li>
        <code>NoDownloadAccount</code> closes the anonymous account developers install from, for a
        gateway that distributes plug another way.
      </li>
    </ul>

    <h3>3. Carry the hosted client</h3>
    <p>
      The anonymous account (<code>ssh get@&lt;gateway&gt; install</code>) hands out what
      <code>/opt/plug</code> holds: the client binaries, their signatures, the installers and the
      version. Copy it from the <strong>hosted</strong> image, the flavour that carries key
      enrolment and leaves updates to the gateway:
    </p>
    <app-code lang="docker">{{ dockerfile }}</app-code>

    <h3>4. Give it the cluster</h3>
    <p>
      The embedded agent needs what a dedicated one needs to serve names: the Docker socket on
      Compose and Swarm, the role of <code>deploy/plug-k8s.yaml</code> on
      <a routerLink="/kubernetes">Kubernetes</a>. With several replicas on Kubernetes, give each one
      its pod's address, so a served name points at the replica that holds the session and any
      replica can tell a live session from a dead one:
    </p>
    <app-code lang="yaml">{{ podIP }}</app-code>
    <p>
      Without it the agent works the address out from the route to the API server, and says so in
      its log when it cannot.
    </p>

    <h3>5. Follow and end sessions</h3>
    <p>
      A plug run opens several connections (the tunnel, the served names, the mounts); each presents
      the run's id, so the gateway sees <strong>one session per run</strong>. A
      <code>SessionEvent</code> carries <code>Who</code>, <code>Key</code> (the SHA256 fingerprint,
      as <code>plug pubkey</code> prints it), <code>From</code>, <code>Version</code>,
      <code>Serves</code> (a <code>-s</code>, not only a <code>-c</code>), <code>Docker</code> (a
      <code>--dockerrun</code>) and <code>Since</code>; at its end, <code>Ended</code>,
      <code>End</code> and <code>Reason</code>. <code>End</code> is <code>ended</code> (the
      developer stopped), <code>dropped</code> (gone without a word and not back within 90 seconds),
      <code>closed</code> (ended on purpose) or <code>shutdown</code> (the agent stopped). Each
      <code>NameEvent</code> names its session.
    </p>
    <p>
      <code>Config.Control</code> is the gateway's hand on the agent:
    </p>
    <app-code lang="go">{{ control }}</app-code>
    <p>
      An ended session is a statement, not a dropped line. The developer reads one line, the
      reason as you gave it, their command stops as Ctrl-C would stop it, plug exits with status 77
      and does not reconnect, and what the session parked is restored at once. The id is refused for
      a few minutes, so a client that missed the notice is told again rather than let back in.
    </p>
    <p>
      With several replicas, a run's connections are spread over them: <strong>ask every
      one</strong>. A replica that never saw the session still refuses it from then on. Merge what
      the replicas report by session id: that is one run.
    </p>
    <p>
      Closing is not a ban: a developer can start a new run. Keeping someone out is
      <code>Verify</code>'s answer, which is why <code>CloseKey</code> exists: withdraw the key, then
      end what it has open, in the same words.
    </p>

    <h3>6. Keep the two on one release</h3>
    <p>
      The package compiled in and the <code>/opt/plug</code> copied in come from the
      <strong>same plug release</strong>. Release tags are plug's own (<code>v&lt;version&gt;</code>),
      not the Go module's, so the module is pinned by the commit of the release:
    </p>
    <app-code lang="bash">{{ pin }}</app-code>
    <p>
      and the image by the same version, <code>softwarity/plug:&lt;version&gt;-hosted</code>. Moving
      one without the other has the gateway announce a version it does not serve.
    </p>

    <h3>Checklist</h3>
    <ul class="check">
      <li>A Host: identity, admission, served names; SessionHost to follow runs.</li>
      <li><code>agent.Start</code> in a goroutine, the verbs answered by <code>agent.Main</code> behind a hidden argument.</li>
      <li><code>Version</code> read from <code>/opt/plug/VERSION</code>, <code>SignpostImage</code>, <code>NoSelfUpdate</code>, <code>KeyInstruction</code>.</li>
      <li><code>/opt/plug</code> copied from the hosted image of the same release.</li>
      <li>The Docker socket or the Kubernetes role; <code>PLUG_POD_IP</code> past one replica.</li>
      <li>A Control, and every replica asked when a session or a key is ended.</li>
    </ul>
  `,
})
export class IntegrationComponent {
  protected readonly host = host;
  protected readonly start = start;
  protected readonly dockerfile = dockerfile;
  protected readonly pin = pin;
  protected readonly podIP = podIP;
  protected readonly control = control;
}
