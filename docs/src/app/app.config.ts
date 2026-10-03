import { ApplicationConfig } from '@angular/core';
import { provideRouter, withHashLocation } from '@angular/router';
import { routes } from './app.routes';

// The release tag is not fetched at runtime: scripts/gen-version.mjs pins it
// into the manifests copied to src/assets at build time, and <app-file> serves
// those. Anything that needs the tag reads it from there.
export const appConfig: ApplicationConfig = {
  providers: [provideRouter(routes, withHashLocation())],
};
