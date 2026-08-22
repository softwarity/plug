import { Injectable, signal } from '@angular/core';

// The deploy snippets show a PINNED image tag - the latest release - instead of
// the moving `:latest`. The tag is resolved at build time into a static resource
// (assets/version.json, see scripts/gen-version.mjs) that this service reads once,
// same-origin, so there is no GitHub API call and no rate limit. The constant is
// only a fallback for `ng serve` or a missing file.
const FALLBACK = '2.0.0';
const TIMEOUT_MS = 2000;

@Injectable({ providedIn: 'root' })
export class VersionService {
  readonly tag = signal<string>(FALLBACK);

  // Awaited at bootstrap (see app.config) so the code blocks are highlighted with
  // the resolved value, not the fallback. Same-origin + tiny → typically instant;
  // the timeout is only a safety net.
  async load(): Promise<void> {
    try {
      const ctl = new AbortController();
      const timer = setTimeout(() => ctl.abort(), TIMEOUT_MS);
      const res = await fetch(new URL('assets/version.json', document.baseURI), {
        signal: ctl.signal,
      });
      clearTimeout(timer);
      if (!res.ok) return;
      const v = String((await res.json())?.version ?? '').trim();
      if (v) this.tag.set(v);
    } catch {
      /* missing file (dev) / offline / timeout → keep the fallback */
    }
  }
}
