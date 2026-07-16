import { ApplicationConfig, inject, provideAppInitializer } from '@angular/core';
import { provideRouter, withHashLocation } from '@angular/router';
import { routes } from './app.routes';
import { VersionService } from './version.service';

export const appConfig: ApplicationConfig = {
  providers: [
    provideRouter(routes, withHashLocation()),
    // Load the build-time version resource before first render, so the deploy
    // snippets are highlighted with the real release tag, not `:latest`.
    provideAppInitializer(() => inject(VersionService).load()),
  ],
};
