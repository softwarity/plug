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

  constructor() {
    // Started, never awaited. It used to block bootstrap so the first render
    // carried the resolved tag; the signal it fills is read by no template, so
    // that was every visitor waiting on a fetch for nothing. Fire and forget:
    // the fallback renders immediately and the real value lands when it lands.
    void this.load();
  }

  // Same-origin and tiny, so typically instant; the timeout is only a safety net
  // for a missing file (dev), an offline visitor, or a slow host.
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
