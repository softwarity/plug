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

// Offline / no-tags fallback for a LOCAL build only (`ng serve` on a shallow
// checkout, a tarball without .git). A build without tags publishes manifests
// pinned to THIS value, so keep it at the current release: the value is what
// `git describe --tags --abbrev=0` prints on main, without the leading `v`.
// In CI there is no fallback: a missing tag there means the checkout is
// shallow (fetch-depth: 0 is required), and the build fails rather than
// publishing a stale pin in silence.
const FALLBACK = '2.20.2';

function latestTag() {
  try {
    const t = execSync('git describe --tags --abbrev=0', { stdio: ['ignore', 'pipe', 'ignore'] })
      .toString()
      .trim()
      .replace(/^v/, '');
    if (t) return t;
  } catch {
    /* no git / no tags reachable: handled below */
  }
  if (process.env.CI) {
    throw new Error(
      'gen-version: no release tag reachable (git describe --tags failed). ' +
        'In CI the checkout must have the tags: use fetch-depth: 0.',
    );
  }
  console.warn(`gen-version: no release tag reachable, falling back to ${FALLBACK} (local build)`);
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

// A failed embed fails the build. The three assets are gitignored, so a source
// that cannot be read would otherwise ship a site whose download buttons serve
// "could not load" in production, with a green build behind it.
function embed(dir, file, transform) {
  try {
    let text = readFileSync(resolve(dir, file), 'utf8').replace(LATEST, pin);
    if (transform) text = transform(text);
    writeFileSync(resolve(assetsDir, file), text);
  } catch (e) {
    throw new Error(`gen-version: could not embed ${file}: ${e.message}`, { cause: e });
  }
  console.log(`gen-version: embedded ${file} (pinned ${version})`);
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
