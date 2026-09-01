import { ApplicationConfig } from '@angular/core';
import { provideRouter, withHashLocation } from '@angular/router';
import { routes } from './app.routes';

// VersionService is gone, and with it assets/version.json.
//
// It fetched the release tag at runtime so the deploy snippets could show a
// pinned image instead of `:latest`. That job was already done, at build time
// and better: scripts/gen-version.mjs rewrites the tag INTO the manifests it
// copies to src/assets, and <app-file> serves those - so the snippet a reader
// copies is pinned, and the file they download is the same one. The service
// filled a signal no template ever read; injected nowhere, `providedIn: 'root'`
// meant it was never even constructed. Whoever needs the tag in a template
// again should read it from the pinned manifests, not fetch it twice.
export const appConfig: ApplicationConfig = {
  providers: [provideRouter(routes, withHashLocation())],
};
