// The sitemap is read from the real routes file: what is proven is that the
// pages a reader can open are the ones a crawler is given, and nothing else.
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { ORIGIN, pagesOf, sitemap } from './gen-sitemap.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const routes = readFileSync(resolve(here, '../src/app/app.routes.ts'), 'utf8');
const meta = readFileSync(resolve(here, '../src/app/page-meta.ts'), 'utf8');

describe('gen-sitemap', () => {
  it('lists every page with a component, and no redirect or catch-all', () => {
    const pages = pagesOf(routes);
    expect(pages).toContain('');
    expect(pages).toContain('kubernetes');
    expect(pages).toContain('security');
    expect(pages).not.toContain('agent');
    expect(pages).not.toContain('**');
    expect(pages.length).toBe((routes.match(/loadComponent/g) ?? []).length);
  });

  it('gives each page its address with a trailing slash, the home page bare', () => {
    const xml = sitemap(ORIGIN, ['', 'cli']);
    expect(xml).toContain('<loc>https://plug.softwarity.dev/</loc>');
    expect(xml).toContain('<loc>https://plug.softwarity.dev/cli/</loc>');
    expect(xml.startsWith('<?xml')).toBe(true);
  });

  it('names the same origin as the canonical links', () => {
    expect(meta).toContain(`siteOrigin = '${ORIGIN}'`);
  });
});
