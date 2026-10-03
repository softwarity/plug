import { provideZonelessChangeDetection } from '@angular/core';
import { bootstrapApplication } from '@angular/platform-browser';
import { AppComponent } from './app/app.component';
import { appConfig } from './app/app.config';
// The <softwarity-projects> menu in the header. A side-effect import: the module
// registers the custom element and exports nothing this app calls. It is bundled
// from the npm package rather than loaded from unpkg at runtime, so the code that
// runs in this site's origin is the version the lockfile pins (bump the package
// to refresh the list of projects it carries), not whatever the CDN serves today.
import '@softwarity/projects';

bootstrapApplication(AppComponent, {
  ...appConfig,
  providers: [provideZonelessChangeDetection(), ...appConfig.providers],
}).catch((err) => console.error(err));
