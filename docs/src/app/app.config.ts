import { ApplicationConfig } from '@angular/core';
import { provideRouter, withHashLocation } from '@angular/router';
import { routes } from './app.routes';

// VersionService is NOT an app initializer any more.
//
// It was awaited before first render so the deploy snippets could show the
// resolved release tag - except no template ever read the signal it fills, so
// every visitor waited on a fetch (up to its 2s timeout) for a value nothing
// displayed. The service now loads itself in the background: whoever wires
// `versions.tag()` into a snippet gets it, and nobody waits for it meanwhile.
export const appConfig: ApplicationConfig = {
  providers: [provideRouter(routes, withHashLocation())],
};
