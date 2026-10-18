import { BootstrapContext, bootstrapApplication } from '@angular/platform-browser';
import { AppComponent } from './app/app.component';
import { config } from './app/app.config.server';

// The build's own entry, never shipped: it renders every route once, at build
// time, so each page is served as HTML a reader, a crawler or a link preview
// can read without running the site's scripts (app.routes.server.ts).
const bootstrap = (context: BootstrapContext) => bootstrapApplication(AppComponent, config, context);

export default bootstrap;
