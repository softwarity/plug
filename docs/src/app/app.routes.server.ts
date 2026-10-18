import { RenderMode, ServerRoute } from '@angular/ssr';

// Every page is static: rendered once at build time into its own index.html,
// which GitHub Pages serves as it is. No server runs anywhere.
export const serverRoutes: ServerRoute[] = [{ path: '**', renderMode: RenderMode.Prerender }];
