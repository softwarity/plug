// Vitest for the Node build scripts (scripts/*.spec.mjs). `ng test` covers the
// Angular code, but it bundles every spec for the browser, where a script that
// shells out to git and writes files cannot run; these run in Node directly.
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['scripts/**/*.spec.mjs'],
  },
});
