import { Component, CUSTOM_ELEMENTS_SCHEMA, ElementRef, viewChild } from '@angular/core';
import { NavigationEnd, Router, RouterOutlet, RouterLink, RouterLinkActive } from '@angular/router';
import { filter } from 'rxjs/operators';
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
  private readonly content = viewChild<ElementRef<HTMLElement>>('content');

  constructor(iconRegistry: MatIconRegistry, router: Router) {
    // Use Material Symbols (loaded in index.html) as the default glyph set for every <mat-icon>.
    iconRegistry.setDefaultFontSetClass('material-symbols-outlined');

    // Move focus into the page after every navigation, and scroll it to the top.
    //
    // A single-page router swaps the content and leaves focus exactly where it
    // was: on the sidebar link that was just clicked. A keyboard user then tabs
    // through the entire sidebar again to reach the page they asked for, and a
    // screen reader says nothing about having arrived anywhere. The route titles
    // handle the announcement; this handles where you are.
    router.events.pipe(filter((e) => e instanceof NavigationEnd)).subscribe(() => {
      const el = this.content()?.nativeElement;
      if (!el) return;
      el.focus({ preventScroll: true });
      el.scrollTo?.({ top: 0 });
      window.scrollTo({ top: 0 });
    });
  }

  protected readonly links: DocLink[] = [
    { path: '/', label: 'About', icon: 'sync_alt' },
    { path: '/getting-started', label: 'Getting started', icon: 'rocket_launch' },
    { path: '/cli', label: 'CLI reference', icon: 'terminal' },
    { path: '/docker', label: 'Docker', icon: 'inventory_2' },
    { path: '/how-it-works', label: 'How it works', icon: 'account_tree' },
    { path: '/profiles', label: 'Profiles & versions', icon: 'settings' },
    { path: '/swarm', label: 'Swarm', icon: 'dns' },
    { path: '/kubernetes', label: 'Kubernetes', icon: 'hub' },
    { path: '/security', label: 'Security model', icon: 'shield' },
    { path: '/troubleshooting', label: 'Troubleshooting', icon: 'troubleshoot' },
    { path: '/continuous-deployment', label: 'CD & GitOps', icon: 'autorenew' },
    { path: '/coverage', label: 'Coverage matrix', icon: 'table_chart' },
    { path: '/roadmap', label: 'Roadmap', icon: 'map' },
  ];
}
