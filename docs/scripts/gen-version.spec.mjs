// gen-version.mjs is run as a process, the way the build runs it, in a
// throwaway tree that mirrors the paths it expects (a docs/scripts/ holding a
// copy of the script, a deploy/ and a docs/snippets/ beside it). The tree is
// what varies: with or without a git tag, with or without the sources to
// embed, with or without CI in the environment. Nothing in the script is
// mocked, so what the tests prove is what the build does.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

const SCRIPT = resolve(dirname(fileURLToPath(import.meta.url)), 'gen-version.mjs');
const FALLBACK = /^const FALLBACK = '(\d+\.\d+\.\d+)';$/m.exec(readFileSync(SCRIPT, 'utf8'))[1];
const LATEST = 'image: docker.io/softwarity/plug:latest';

let root;

function tree({ sources = true } = {}) {
  root = mkdtempSync(join(tmpdir(), 'gen-version-'));
  mkdirSync(join(root, 'docs/scripts'), { recursive: true });
  writeFileSync(join(root, 'docs/scripts/gen-version.mjs'), readFileSync(SCRIPT));
  if (sources) {
    mkdirSync(join(root, 'deploy'));
    mkdirSync(join(root, 'docs/snippets'));
    writeFileSync(join(root, 'deploy/plug-stack.yml'), `services:\n  plug:\n    ${LATEST}\n`);
    writeFileSync(
      join(root, 'deploy/plug-k8s.yaml'),
      `containers:\n  - ${LATEST}\n    imagePullPolicy: Always # the moving tag\n`,
    );
    writeFileSync(join(root, 'docs/snippets/plug-service.yml'), `  plug:\n    ${LATEST}\n`);
  }
  return root;
}

// The script runs with the tree as its working directory: `git describe`
// looks there, and a temp dir is no repository unless a test makes it one.
function run({ ci = false } = {}) {
  const env = { ...process.env };
  delete env.CI;
  delete env.GIT_DIR;
  if (ci) env.CI = 'true';
  const r = spawnSync(process.execPath, [join(root, 'docs/scripts/gen-version.mjs')], {
    cwd: root,
    env,
    encoding: 'utf8',
  });
  return { status: r.status, out: r.stdout, err: r.stderr };
}

function git(...args) {
  const r = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
  if (r.status !== 0) throw new Error(`git ${args.join(' ')}: ${r.stderr}`);
  return r.stdout.trim();
}

function asset(name) {
  return readFileSync(join(root, 'docs/src/assets', name), 'utf8');
}

describe('gen-version', () => {
  beforeEach(() => tree());
  afterEach(() => rmSync(root, { recursive: true, force: true }));

  it('pins the latest reachable tag into every embedded asset', () => {
    git('init', '-q');
    git('-c', 'user.name=t', '-c', 'user.email=t@t', 'commit', '-q', '--allow-empty', '-m', 'one');
    git('tag', 'v9.8.7');
    git('-c', 'user.name=t', '-c', 'user.email=t@t', 'commit', '-q', '--allow-empty', '-m', 'two');

    const r = run();
    expect(r.status).toBe(0);
    expect(r.err).toBe('');
    expect(r.out).toContain('embedded plug-stack.yml (pinned 9.8.7)');
    expect(asset('plug-stack.yml')).toContain('image: docker.io/softwarity/plug:9.8.7');
    expect(asset('plug-service.yml')).toContain('image: docker.io/softwarity/plug:9.8.7');
    const k8s = asset('plug-k8s.yaml');
    expect(k8s).toContain('image: docker.io/softwarity/plug:9.8.7');
    // With an exact tag the pull policy for a moving one makes no sense.
    expect(k8s).toContain('imagePullPolicy: IfNotPresent # pinned tag');
    expect(k8s).not.toContain('Always');
    expect(k8s).not.toContain('latest');
  });

  it('falls back to the pinned constant outside CI, and says so', () => {
    const r = run();
    expect(r.status).toBe(0);
    expect(r.err).toContain(`falling back to ${FALLBACK} (local build)`);
    expect(asset('plug-stack.yml')).toContain(`image: docker.io/softwarity/plug:${FALLBACK}`);
  });

  it('fails under CI when no tag is reachable, before writing anything', () => {
    const r = run({ ci: true });
    expect(r.status).not.toBe(0);
    expect(r.err).toContain('no release tag reachable');
    expect(r.err).toContain('fetch-depth: 0');
    expect(existsSync(join(root, 'docs/src/assets/plug-stack.yml'))).toBe(false);
  });

  it('fails when a source to embed cannot be read', () => {
    rmSync(root, { recursive: true, force: true });
    tree({ sources: false });

    const r = run();
    expect(r.status).not.toBe(0);
    expect(r.err).toContain('could not embed plug-stack.yml');
    expect(r.err).toContain('ENOENT');
  });

  it('fails on the one source that is missing, after embedding the ones before it', () => {
    rmSync(join(root, 'docs/snippets/plug-service.yml'));

    const r = run();
    expect(r.status).not.toBe(0);
    expect(r.err).toContain('could not embed plug-service.yml');
    expect(r.out).toContain('embedded plug-k8s.yaml');
    expect(existsSync(join(root, 'docs/src/assets/plug-service.yml'))).toBe(false);
  });
});
