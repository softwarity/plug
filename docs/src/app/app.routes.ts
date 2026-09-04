import { Routes } from '@angular/router';

// A title per route, so the document title changes when the page does.
//
// It did not. On a single-page site the browser tab, the history entry and,
// most of all, what a screen reader announces on navigation all come
// from document.title, and it stayed "plug" from the first load to the last.
// Angular's default TitleStrategy applies these; nothing else is needed.

export const routes: Routes = [
  {
    path: '',
    title: 'plug - About',
    pathMatch: 'full',
    loadComponent: () => import('./pages/about.component').then((m) => m.AboutComponent),
  },
  {
    path: 'getting-started',
    title: 'plug - Getting started',
    loadComponent: () => import('./pages/getting-started.component').then((m) => m.GettingStartedComponent),
  },
  {
    path: 'cli',
    title: 'plug - CLI reference',
    loadComponent: () => import('./pages/cli.component').then((m) => m.CliComponent),
  },
  {
    path: 'how-it-works',
    title: 'plug - How it works',
    loadComponent: () => import('./pages/how-it-works.component').then((m) => m.HowItWorksComponent),
  },
  {
    path: 'profiles',
    title: 'plug - Profiles & versions',
    loadComponent: () => import('./pages/profiles.component').then((m) => m.ProfilesComponent),
  },
  {
    path: 'swarm',
    title: 'plug - Swarm',
    loadComponent: () => import('./pages/agent-swarm.component').then((m) => m.AgentSwarmComponent),
  },
  {
    path: 'kubernetes',
    title: 'plug - Kubernetes',
    loadComponent: () =>
      import('./pages/agent-kubernetes.component').then((m) => m.AgentKubernetesComponent),
  },
  {
    path: 'troubleshooting',
    title: 'plug - Troubleshooting',
    loadComponent: () =>
      import('./pages/troubleshooting.component').then((m) => m.TroubleshootingComponent),
  },
  {
    path: 'continuous-deployment',
    title: 'plug - CD & GitOps',
    loadComponent: () =>
      import('./pages/continuous-deployment.component').then((m) => m.ContinuousDeploymentComponent),
  },
  { path: 'agent', redirectTo: 'swarm', pathMatch: 'full' },
  {
    path: 'security',
    title: 'plug - Security model',
    loadComponent: () => import('./pages/security.component').then((m) => m.SecurityComponent),
  },
  {
    path: 'roadmap',
    title: 'plug - Roadmap',
    loadComponent: () => import('./pages/roadmap.component').then((m) => m.RoadmapComponent),
  },
  {
    path: 'coverage',
    title: 'plug - Coverage matrix',
    loadComponent: () => import('./pages/coverage.component').then((m) => m.CoverageComponent),
  },
  { path: '**', redirectTo: '' },
];
