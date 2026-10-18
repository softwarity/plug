import { bootstrapApplication } from '@angular/platform-browser';
import { AppComponent } from './app/app.component';
import { appConfig } from './app/app.config';
// The <softwarity-projects> menu in the header. A side-effect import: the module
// registers the custom element and exports nothing this app calls. It is bundled
// from the npm package rather than loaded from unpkg at runtime, so the code that
// runs in this site's origin is the version the lockfile pins (bump the package
// to refresh the list of projects it carries), not whatever the CDN serves today.
import '@softwarity/projects';

// The site routed on the hash until it had a domain of its own, and those
// addresses are still out there: in bookmarks, in other sites, in the messages
// plug and its agent print. /#/kubernetes becomes /kubernetes, an anchor after it
// included, before the router reads the address.
if (location.hash.startsWith('#/')) {
  history.replaceState(history.state, '', location.hash.slice(1));
}

bootstrapApplication(AppComponent, appConfig).catch((err) => console.error(err));
