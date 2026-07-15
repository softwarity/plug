import { Routes } from '@angular/router';

export const routes: Routes = [
  {
    path: '',
    pathMatch: 'full',
    loadComponent: () => import('./pages/about.component').then((m) => m.AboutComponent),
  },
  {
    path: 'getting-started',
    loadComponent: () => import('./pages/getting-started.component').then((m) => m.GettingStartedComponent),
  },
  {
    path: 'how-it-works',
    loadComponent: () => import('./pages/how-it-works.component').then((m) => m.HowItWorksComponent),
  },
  {
    path: 'profiles',
    loadComponent: () => import('./pages/profiles.component').then((m) => m.ProfilesComponent),
  },
  {
    path: 'swarm',
    loadComponent: () => import('./pages/agent-swarm.component').then((m) => m.AgentSwarmComponent),
  },
  {
    path: 'kubernetes',
    loadComponent: () =>
      import('./pages/agent-kubernetes.component').then((m) => m.AgentKubernetesComponent),
  },
  { path: 'agent', redirectTo: 'swarm', pathMatch: 'full' },
  {
    path: 'security',
    loadComponent: () => import('./pages/security.component').then((m) => m.SecurityComponent),
  },
  {
    path: 'roadmap',
    loadComponent: () => import('./pages/roadmap.component').then((m) => m.RoadmapComponent),
  },
  {
    path: 'coverage',
    loadComponent: () => import('./pages/coverage.component').then((m) => m.CoverageComponent),
  },
  { path: '**', redirectTo: '' },
];
