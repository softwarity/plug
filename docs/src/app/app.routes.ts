import { Routes } from '@angular/router';

// A title per route: on a single-page site the browser tab, the history entry
// and what a screen reader announces on navigation all come from
// document.title. Angular's default TitleStrategy applies these. The
// description is the page's meta description, written into its pre-rendered
// HTML with its canonical address (page-meta.ts).

export const routes: Routes = [
  {
    path: '',
    title: 'plug - A local process as a member of your cluster',
    data: { description: "plug runs a local process as a member of your cluster: it resolves the cluster's service names, reaches its services, and is reachable under a name of its own, with no change to the application. Docker, Compose, Swarm, Kubernetes, OpenShift." },
    pathMatch: 'full',
    loadComponent: () => import('./pages/about.component').then((m) => m.AboutComponent),
  },
  {
    path: 'getting-started',
    title: 'plug - Getting started',
    data: { description: 'Deploy the plug agent on a cluster, install the CLI from it in one command, and run your first local process as a member of the cluster.' },
    loadComponent: () => import('./pages/getting-started.component').then((m) => m.GettingStartedComponent),
  },
  {
    path: 'use-cases',
    title: 'plug - Use cases',
    data: { description: 'Scenarios where plug fits: debugging against a real cluster, a front end on the laptop, QA trying a branch before its pull request, two developers or AI agents on one feature.' },
    loadComponent: () => import('./pages/use-cases.component').then((m) => m.UseCasesComponent),
  },
  {
    path: 'cli',
    title: 'plug - CLI reference',
    data: { description: 'Every plug command and option: serving a name with -s, pure client with -c, profiles, environment and volume projection, doctor, update.' },
    loadComponent: () => import('./pages/cli.component').then((m) => m.CliComponent),
  },
  {
    path: 'build-tools',
    title: 'plug - From your build tool',
    data: { description: 'Running plug from npm, Maven, Gradle, Composer, Deno, PDM, Hatch, Make or just, and from the IDE with a debugger attached.' },
    loadComponent: () => import('./pages/build-tools.component').then((m) => m.BuildToolsComponent),
  },
  {
    path: 'comparison',
    title: 'plug - How it compares',
    data: { description: "How plug compares with mirrord and Telepresence, row by row, from each tool's public documentation." },
    loadComponent: () => import('./pages/comparison.component').then((m) => m.ComparisonComponent),
  },
  {
    path: 'how-it-works',
    title: 'plug - How it works',
    data: { description: 'How plug works: one SSH connection carries both directions, a deployed service is parked for the session, its volumes are mounted live, and the session cleans up after itself.' },
    loadComponent: () => import('./pages/how-it-works.component').then((m) => m.HowItWorksComponent),
  },
  {
    path: 'under-the-hood',
    title: 'plug - Under the hood',
    data: { description: "plug's data path step by step: a userspace TUN on your machine, SSH to an agent in the cluster, the privilege granted once, several clusters at once." },
    loadComponent: () => import('./pages/under-the-hood.component').then((m) => m.UnderTheHoodComponent),
  },
  {
    path: 'profiles',
    title: 'plug - Profiles & versions',
    data: { description: "plug profiles: one per cluster, automatic selection, split-horizon DNS, and the launcher that runs the version each cluster's agent serves." },
    loadComponent: () => import('./pages/profiles.component').then((m) => m.ProfilesComponent),
  },
  {
    path: 'swarm',
    title: 'plug - Docker, Compose and Swarm',
    data: { description: 'Deploy the plug agent on Docker, Compose or Swarm: in your stack or standalone, the Docker socket it needs, image tags, and how it publishes a name in the cluster.' },
    loadComponent: () => import('./pages/agent-swarm.component').then((m) => m.AgentSwarmComponent),
  },
  {
    path: 'kubernetes',
    title: 'plug - Kubernetes and OpenShift',
    data: { description: 'Deploy the plug agent on Kubernetes, OpenShift or OKD: the manifest, the namespace-scoped Role rule by rule, and how to reach the agent.' },
    loadComponent: () =>
      import('./pages/agent-kubernetes.component').then((m) => m.AgentKubernetesComponent),
  },
  {
    path: 'mcp',
    title: 'plug - MCP for AI coding agents',
    data: { description: "plug mcp: an MCP server that gives an AI coding agent the clusters a machine knows, doctor checks, a workload's environment and DNS lookups." },
    loadComponent: () => import('./pages/mcp.component').then((m) => m.McpComponent),
  },
  {
    path: 'troubleshooting',
    title: 'plug - Troubleshooting',
    data: { description: 'Fixes for the problems plug users meet: an agent that will not start, a name nothing reaches, a service that did not come back, unknown hosts.' },
    loadComponent: () =>
      import('./pages/troubleshooting.component').then((m) => m.TroubleshootingComponent),
  },
  {
    path: 'continuous-deployment',
    title: 'plug - CD & GitOps',
    data: { description: 'plug alongside Argo CD, Flux, Rancher Fleet and operators: why a GitOps controller reverts a takeover, and the setting that lets both coexist.' },
    loadComponent: () =>
      import('./pages/continuous-deployment.component').then((m) => m.ContinuousDeploymentComponent),
  },
  { path: 'agent', redirectTo: 'swarm', pathMatch: 'full' },
  {
    path: 'security',
    title: 'plug - Security model',
    data: { description: "plug's security model: who may open a session (open, by approval, hosted), what a session can reach once in, the Docker socket, signed binaries." },
    loadComponent: () => import('./pages/security.component').then((m) => m.SecurityComponent),
  },
  {
    path: 'meerkat',
    title: 'plug - Meerkat',
    data: { description: 'Meerkat, the app gateway that embeds the plug agent: sessions tied to named developers, visible to everyone using the cluster.' },
    loadComponent: () => import('./pages/meerkat.component').then((m) => m.MeerkatComponent),
  },
  {
    path: 'integration',
    title: 'plug - Integration',
    data: { description: 'How a gateway embeds the plug agent as a Go package: the Host it implements, Start, the hosted client, cluster rights, following and ending sessions.' },
    loadComponent: () => import('./pages/integration.component').then((m) => m.IntegrationComponent),
  },
  {
    path: 'roadmap',
    title: 'plug - Roadmap',
    data: { description: 'What is planned for plug, and what is left out on purpose.' },
    loadComponent: () => import('./pages/roadmap.component').then((m) => m.RoadmapComponent),
  },
  {
    path: 'coverage',
    title: 'plug - Coverage matrix',
    data: { description: "plug's coverage matrix: what is proven in CI on each cluster backend and each client OS, and what is not proven yet." },
    loadComponent: () => import('./pages/coverage.component').then((m) => m.CoverageComponent),
  },
  { path: '**', redirectTo: '' },
];
