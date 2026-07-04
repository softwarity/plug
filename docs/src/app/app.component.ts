import { Component, CUSTOM_ELEMENTS_SCHEMA } from '@angular/core';
import { RouterOutlet, RouterLink, RouterLinkActive } from '@angular/router';
import { IconComponent } from './icon/icon.component';

interface DocLink {
  path: string;
  label: string;
  icon: string;
}

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, RouterLink, RouterLinkActive, IconComponent],
  schemas: [CUSTOM_ELEMENTS_SCHEMA],
  templateUrl: './app.component.html',
  styleUrl: './app.component.scss',
})
export class AppComponent {
  protected readonly links: DocLink[] = [
    { path: '/', label: 'Getting started', icon: 'rocket_launch' },
    { path: '/how-it-works', label: 'How it works', icon: 'account_tree' },
    { path: '/profiles', label: 'Profiles & versions', icon: 'settings' },
    { path: '/agent', label: 'Agent & deployment', icon: 'dns' },
    { path: '/security', label: 'Security model', icon: 'shield' },
    { path: '/roadmap', label: 'Roadmap', icon: 'map' },
  ];
}
