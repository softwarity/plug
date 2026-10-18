import { DOCUMENT, inject, Injectable } from '@angular/core';
import { Meta } from '@angular/platform-browser';
import { ActivatedRouteSnapshot, NavigationEnd, Router } from '@angular/router';
import { filter } from 'rxjs/operators';

// The address the site is served from. Canonical links and link previews name
// it, whatever host the page was opened on (a mirror, a preview, localhost).
export const siteOrigin = 'https://plug.softwarity.dev';

/**
 * Writes each page's own description and canonical address into the document,
 * on every navigation. It runs when the pages are rendered at build time too,
 * so the HTML a crawler or a link preview reads carries them, not only the
 * page a browser has finished drawing.
 */
@Injectable({ providedIn: 'root' })
export class PageMeta {
  private readonly meta = inject(Meta);
  private readonly doc = inject(DOCUMENT);
  private readonly router = inject(Router);

  start(): void {
    this.router.events.pipe(filter((e) => e instanceof NavigationEnd)).subscribe(() => {
      let route: ActivatedRouteSnapshot = this.router.routerState.snapshot.root;
      while (route.firstChild) route = route.firstChild;
      const description = route.data['description'] as string | undefined;
      const path = this.router.url.split(/[?#]/)[0];
      // With its trailing slash: each page is <page>/index.html, and GitHub
      // Pages answers /page by redirecting to /page/.
      const url = siteOrigin + (path === '/' ? '/' : path + '/');
      if (description) {
        this.meta.updateTag({ name: 'description', content: description });
        this.meta.updateTag({ property: 'og:description', content: description });
        this.meta.updateTag({ name: 'twitter:description', content: description });
      }
      const title = this.doc.title;
      if (title) {
        this.meta.updateTag({ property: 'og:title', content: title });
        this.meta.updateTag({ name: 'twitter:title', content: title });
      }
      this.meta.updateTag({ property: 'og:url', content: url });
      this.meta.updateTag({ name: 'twitter:url', content: url });
      this.canonical(url);
    });
  }

  private canonical(url: string): void {
    let link = this.doc.head.querySelector<HTMLLinkElement>('link[rel="canonical"]');
    if (!link) {
      link = this.doc.createElement('link');
      link.setAttribute('rel', 'canonical');
      this.doc.head.appendChild(link);
    }
    link.setAttribute('href', url);
  }
}
