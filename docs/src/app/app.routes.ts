import { Routes } from '@angular/router';

export const routes: Routes = [
  {
    path: '',
    pathMatch: 'full',
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
    path: 'agent',
    loadComponent: () => import('./pages/agent.component').then((m) => m.AgentComponent),
  },
  {
    path: 'security',
    loadComponent: () => import('./pages/security.component').then((m) => m.SecurityComponent),
  },
  {
    path: 'roadmap',
    loadComponent: () => import('./pages/roadmap.component').then((m) => m.RoadmapComponent),
  },
  { path: '**', redirectTo: '' },
];
