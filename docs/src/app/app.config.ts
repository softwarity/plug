import { ApplicationConfig, provideZonelessChangeDetection } from '@angular/core';
import { provideRouter, withInMemoryScrolling } from '@angular/router';
import { routes } from './app.routes';

// The release tag is not fetched at runtime: scripts/gen-version.mjs pins it
// into the manifests copied to src/assets at build time, and <app-file> serves
// those. Anything that needs the tag reads it from there.
//
// Routed on the path (/kubernetes), so each page has an address of its own that
// is rendered at build time; addresses from the hash era (/#/kubernetes) are
// moved onto it before the app starts (main.ts).
export const appConfig: ApplicationConfig = {
  providers: [
    provideZonelessChangeDetection(),
    provideRouter(routes, withInMemoryScrolling({ anchorScrolling: 'enabled' })),
  ],
};
