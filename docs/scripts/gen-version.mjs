// Resolve the latest release tag at BUILD time and pin it INTO the manifests and
// snippets the doc-site serves, so a reader copies a real version instead of the
// moving `:latest`. Run by the `build`/`start` npm scripts, so it fires on every
// build (local and CI). CI must check out with fetch-depth: 0 so the tags are
// present.
//
// It used to also emit src/assets/version.json for a runtime fetch. Nothing read
// it: the only consumer, VersionService, was injected nowhere and so never even
// constructed, and the pinning below already puts the tag where it shows.
import { execSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// Offline / no-tags fallback (e.g. `ng serve`, a shallow checkout). Nothing
// overrides it any more, so a build without tags publishes manifests pinned to
// THIS value: keep it at the current release.
const FALLBACK = '2.13.0';

function latestTag() {
  try {
    const t = execSync('git describe --tags --abbrev=0', { stdio: ['ignore', 'pipe', 'ignore'] })
      .toString()
      .trim()
      .replace(/^v/, '');
    if (t) return t;
  } catch {
    /* no git / no tags reachable → fall back */
  }
  return FALLBACK;
}

const version = latestTag();
const scriptDir = dirname(fileURLToPath(import.meta.url));
const assetsDir = resolve(scriptDir, '../src/assets');
mkdirSync(assetsDir, { recursive: true });

// Embed doc-facing manifests/snippets as pinned, downloadable assets: copy each
// source and swap the moving `:latest` for this exact release, so the doc shows
// (and serves) a real version instead of `latest`. The repo files stay `:latest`
// templates, this is a build-time copy, à la Maven resource filtering.
const deployDir = resolve(scriptDir, '../../deploy'); // real, standalone deployable manifests
const snippetsDir = resolve(scriptDir, '../snippets'); // doc-only illustrative fragments
const LATEST = /docker\.io\/softwarity\/plug:latest/g;
const pin = `docker.io/softwarity/plug:${version}`;

function embed(dir, file, transform) {
  try {
    let text = readFileSync(resolve(dir, file), 'utf8').replace(LATEST, pin);
    if (transform) text = transform(text);
    writeFileSync(resolve(assetsDir, file), text);
    console.log(`gen-version: embedded ${file} (pinned ${version})`);
  } catch (e) {
    console.warn(`gen-version: could not embed ${file}: ${e.message}`);
  }
}

embed(deployDir, 'plug-stack.yml');
// With an exact tag, `imagePullPolicy: Always` (which existed for the moving
// `latest`) is no longer needed: pin it too so the served manifest is coherent.
embed(deployDir, 'plug-k8s.yaml', (t) =>
  t.replace(/imagePullPolicy: Always.*$/m, 'imagePullPolicy: IfNotPresent # pinned tag'),
);
// The "add this to your existing stack" snippet shown on Getting started AND
// Swarm: one source, so the two pages can never drift apart.
embed(snippetsDir, 'plug-service.yml');
