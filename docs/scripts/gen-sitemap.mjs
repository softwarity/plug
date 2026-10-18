// Writes src/root/sitemap.xml from the router's own list of pages, so a page
// added to app.routes.ts is in the sitemap without anyone remembering it. Run
// by the `build`/`start` npm scripts, before Angular builds; the file is served
// at the site's root beside robots.txt (angular.json, assets).
//
// A page is a route with a component. Redirects (an old address kept working)
// and the catch-all are not pages, and listing them would hand a crawler
// addresses that only bounce. Each address ends with a slash: the pages are
// pre-rendered into <page>/index.html, and GitHub Pages answers /page with a
// redirect to /page/.
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// The same origin the pages name as canonical (src/app/page-meta.ts).
export const ORIGIN = 'https://plug.softwarity.dev';

export function pagesOf(routesSource) {
  const found = [...routesSource.matchAll(/\bpath: '([^']*)'/g)];
  return found
    .filter((m, i) => routesSource.slice(m.index, found[i + 1]?.index).includes('loadComponent'))
    .map((m) => m[1]);
}

export function sitemap(origin, pages) {
  const urls = pages.map((p) => `  <url><loc>${origin}/${p ? p + '/' : ''}</loc></url>`).join('\n');
  return `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls}\n</urlset>\n`;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const docs = resolve(dirname(fileURLToPath(import.meta.url)), '..');
  const pages = pagesOf(readFileSync(resolve(docs, 'src/app/app.routes.ts'), 'utf8'));
  writeFileSync(resolve(docs, 'src/root/sitemap.xml'), sitemap(ORIGIN, pages));
  console.log(`sitemap.xml: ${pages.length} pages`);
}
