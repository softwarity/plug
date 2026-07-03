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
    { path: '/', label: 'Getting started', icon: 'rocket_launch' },
    { path: '/how-it-works', label: 'How it works', icon: 'account_tree' },
    { path: '/profiles', label: 'Profiles & wizard', icon: 'settings' },
    { path: '/agent', label: 'Agent & deployment', icon: 'dns' },
    { path: '/security', label: 'Security model', icon: 'shield' },
    { path: '/roadmap', label: 'Roadmap', icon: 'map' },
  ];
}
