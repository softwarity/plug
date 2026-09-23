import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { CodeComponent } from '../code/code.component';

@Component({
  selector: 'app-mcp',
  imports: [CodeComponent, RouterLink],
  preserveWhitespaces: true,
  template: `
    <h2>MCP: plug for your AI coding agent</h2>

    <p>
      What an agent lacks is not the ability to run <code>plug -s …</code>; it is knowing what to
      run and reading what came of it. Which clusters this machine knows, what
      <code>doctor</code> finds and the exact remedy, the environment a deployed workload runs
      with, whether a name exists in the cluster: plug knows all of that, and answered in prose
      for a person. <code>plug mcp</code> answers with structure, as
      <a href="https://modelcontextprotocol.io" target="_blank" rel="noopener">MCP</a> tools.
    </p>
    <p>
      It runs over stdio, the way editors launch an MCP server: one process per editor session,
      nothing listening on a port. It is the plug launcher itself, so one entry covers every
      profile on the machine, whatever version each cluster runs - the launcher already follows
      each cluster's agent (see <a routerLink="/profiles">Profiles &amp; versions</a>).
    </p>

    <h3>Install it in your agent</h3>
    <p>The command is <code>plug</code>, the only argument is <code>mcp</code>. Same everywhere:</p>

    <h4>Claude Code</h4>
    <app-code lang="bash">claude mcp add --scope user plug -- plug mcp</app-code>
    <p>
      <code>--scope user</code> makes it available in every project; leave it out for the current
      one, or <code>--scope project</code> to share it through <code>.mcp.json</code> at the
      project root:
    </p>
    <app-code lang="json">{{ '{' }}
  "mcpServers": {{ '{' }}
    "plug": {{ '{' }} "type": "stdio", "command": "plug", "args": ["mcp"] {{ '}' }}
  {{ '}' }}
{{ '}' }}</app-code>

    <h4>Cursor</h4>
    <p><code>.cursor/mcp.json</code> in the project, or <code>~/.cursor/mcp.json</code> for all of them:</p>
    <app-code lang="json">{{ '{' }}
  "mcpServers": {{ '{' }}
    "plug": {{ '{' }} "type": "stdio", "command": "plug", "args": ["mcp"] {{ '}' }}
  {{ '}' }}
{{ '}' }}</app-code>

    <h4>VS Code (Copilot)</h4>
    <p>
      <code>.vscode/mcp.json</code> in the workspace, or the command palette's
      <em>MCP: Open User Configuration</em> for every workspace. The top-level key is
      <code>servers</code>, not <code>mcpServers</code>:
    </p>
    <app-code lang="json">{{ '{' }}
  "servers": {{ '{' }}
    "plug": {{ '{' }} "command": "plug", "args": ["mcp"] {{ '}' }}
  {{ '}' }}
{{ '}' }}</app-code>

    <h4>Windsurf, and the others</h4>
    <p>
      Any client that launches stdio servers takes the same three fields. Windsurf reads
      <code>mcp_config.json</code> under its config directory with the <code>mcpServers</code>
      shape above.
    </p>

    <p>
      <code>plug</code> has to be on the agent's <code>PATH</code>, which is the case once it is
      installed from a cluster (<a routerLink="/getting-started">Getting started</a>). On macOS the
      launcher is setuid; the MCP server never needs that privilege and never uses it: the tools
      read, they do not open a tunnel.
    </p>

    <h3>The tools</h3>
    <table class="matrix">
      <thead><tr><th>tool</th><th>what it answers</th></tr></thead>
      <tbody>
        <tr><td><code>list_profiles</code></td><td>the clusters this machine knows: name, agent address, update policy, whether a personal key is enrolled</td></tr>
        <tr><td><code>doctor</code></td><td><code>plug doctor</code>, check by check: area, name, <code>ok</code> / <code>warn</code> / <code>fail</code>, the detail and the exact remedy. Optionally for one profile</td></tr>
        <tr><td><code>env_of</code></td><td>the environment a deployed workload runs with, as a <code>-s</code> takeover would hand it to your command (see <a routerLink="/how-it-works">How it works</a>). Secret-looking values are masked; <code>reveal: true</code> asks for them in the clear</td></tr>
        <tr><td><code>resolve_name</code></td><td>whether a name exists in the cluster, through the cluster's own DNS: what <code>-s</code> would take over, or what a session would reach</td></tr>
        <tr><td><code>agent_info</code></td><td>the agent's report: version, backend, image, and on Kubernetes the RBAC grants it probed (<code>endpoints</code>, <code>exec</code>)</td></tr>
      </tbody>
    </table>

    <p>
      Every tool is a verb the CLI already speaks or the doctor it already runs; nothing is new
      on the agent side, and an agent older than a verb answers "unknown command", which the
      tool returns as an error naming the profile to update.
    </p>

    <h3>What it looks like from the agent</h3>
    <p>Ask "why does orders-svc not reach odb when I plug it on prod?" and the agent can read, in order:</p>
    <ul>
      <li><code>doctor</code> on <code>prod</code>: <code>exec grant: warn</code>, "the agent may not exec into pods: a plugged service gets its variables from the pod spec, and those that come from a Secret arrive empty", with the <code>kubectl patch</code> to run;</li>
      <li><code>env_of orders-svc</code>: <code>DB_PASSWORD</code> is in the notes as "empty because it comes from a Secret";</li>
      <li>and conclude, and fix the role, without having been told how plug works.</li>
    </ul>

    <h3>Not yet</h3>
    <p>
      Opening a session from a tool (<code>serve</code> / <code>unserve</code>) is not in this
      version: a session is a process that lives, and it deserves its own shape. See the
      <a routerLink="/roadmap">roadmap</a>.
    </p>
  `,
})
export class McpComponent {}
