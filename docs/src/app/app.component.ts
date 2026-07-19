import { Component, CUSTOM_ELEMENTS_SCHEMA } from '@angular/core';
import { RouterOutlet, RouterLink, RouterLinkActive } from '@angular/router';
import { MatIconModule, MatIconRegistry } from '@angular/material/icon';

interface DocLink {
  path: string;
  label: string;
  icon: string;
}

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, RouterLink, RouterLinkActive, MatIconModule],
  schemas: [CUSTOM_ELEMENTS_SCHEMA],
  templateUrl: './app.component.html',
  styleUrl: './app.component.scss',
})
export class AppComponent {
  constructor(iconRegistry: MatIconRegistry) {
    // Use Material Symbols (loaded in index.html) as the default glyph set for every <mat-icon>.
    iconRegistry.setDefaultFontSetClass('material-symbols-outlined');
  }

  protected readonly links: DocLink[] = [
    { path: '/', label: 'About', icon: 'sync_alt' },
    { path: '/getting-started', label: 'Getting started', icon: 'rocket_launch' },
    { path: '/cli', label: 'CLI reference', icon: 'terminal' },
    { path: '/how-it-works', label: 'How it works', icon: 'account_tree' },
    { path: '/profiles', label: 'Profiles & versions', icon: 'settings' },
    { path: '/swarm', label: 'Swarm', icon: 'dns' },
    { path: '/kubernetes', label: 'Kubernetes', icon: 'hub' },
    { path: '/security', label: 'Security model', icon: 'shield' },
    { path: '/coverage', label: 'Coverage matrix', icon: 'table_chart' },
    { path: '/roadmap', label: 'Roadmap', icon: 'map' },
  ];
}
