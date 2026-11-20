// file: core/web/scripts/presets.test.mjs
// version: 0.1.0
// guid: 4e9b1d73-6a20-4c58-8f37-b0d2e5a9c164
// last-edited: 2026-11-20

// 07 §7.18 presets.test.mjs (FE8-1): the presets are unbuilt source every
// config loads; from a clean core/web (no dist/) api-client, ui-kit and shell
// build in the §7.3.2 order with no prior step; shell/dist carries the
// MEDIACORE_CORE_VERSION literal; mediacoreLibrary writes no HTML and takes no
// appId; mediacore() validates its options and writes the three metas and the
// boot script ui-kit's themeBootScript returns. Run: node --test scripts/presets.test.mjs

import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import * as presets from '@mediacore/shell/vite';
// Dependency-free TS, loaded with Node's type stripping: ui-kit's source, not a
// package export, because the point is that the preset and ui-kit emit one script.
// eslint-disable-next-line import-x/no-relative-packages
import { themeBootScript as kitBootScript } from '../packages/ui-kit/src/theme/boot.ts';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pkgs = ['api-client', 'ui-kit', 'shell'];

test('every package config loads its preset from source, not a dist/', () => {
  const kit = readFileSync(join(root, 'packages/ui-kit/vite.config.ts'), 'utf8');
  const shell = readFileSync(join(root, 'packages/shell/vite.config.ts'), 'utf8');
  assert.match(kit, /from '@mediacore\/shell\/vite'/);
  assert.match(shell, /from '\.\/vite\/index\.mjs'/);
  const exp = JSON.parse(readFileSync(join(root, 'packages/shell/package.json'), 'utf8')).exports['./vite'];
  assert.equal(exp.default, './vite/index.mjs');
  for (const cfg of [kit, shell]) assert.doesNotMatch(cfg, /\/dist\//);
});

test('mediacoreLibrary writes no HTML and takes no appId', () => {
  const cfg = presets.mediacoreLibrary({ name: '@mediacore/ui-kit' });
  assert.ok(cfg.build.lib, 'library mode');
  assert.ok(!cfg.plugins.some((p) => p && p.transformIndexHtml), 'no HTML transform');
  assert.equal(cfg.define, undefined, 'ui-kit gets no core-version define');
  assert.throws(() => presets.mediacoreLibrary({ name: 'hello' }), /must be @mediacore\/ui-kit or @mediacore\/shell/);
  assert.throws(() => presets.mediacoreLibrary({ appId: 'hello' }), /name must be/);
});

test('mediacore() refuses an appId or themeStorageKey outside its pattern', () => {
  for (const appId of ['', 'H', 'Hello', '1abc', 'a', 'abcdefghijklmnopq', 'bat-cave']) {
    assert.throws(() => presets.mediacore({ appId }), /appId/, appId);
  }
  assert.throws(() => presets.mediacore({ appId: 'gotham', themeStorageKey: 'bad key' }), /themeStorageKey/);
  assert.throws(() => presets.mediacore({ appId: 'gotham', themeStorageKey: 'x'.repeat(129) }), /themeStorageKey/);
  assert.throws(() => presets.mediacore({ appId: 'gotham', defaultMode: 'sepia' }), /defaultMode/);
});

test('the app preset writes the build, app and theme metas and the boot script', () => {
  const tags = presets.headTags({ buildId: 'b4t-1', appId: 'gotham', themeStorageKey: 'gotham.shell.themeMode', defaultMode: 'dark' });
  assert.deepEqual(tags.slice(0, 3).map((t) => t.attrs), [
    { name: 'mediacore-build', content: 'b4t-1' },
    { name: 'mediacore-app', content: 'gotham' },
    { name: 'mediacore-theme', content: 'gotham.shell.themeMode dark' },
  ]);
  assert.equal(tags[3].tag, 'script');
  assert.equal(tags[3].children, kitBootScript('gotham.shell.themeMode', 'dark'), 'one boot script for the preset and ui-kit');
  assert.equal(presets.themeBootScript('k', 'light'), kitBootScript('k', 'light'));
});

test('resolveBuildId follows the §7.6.3 rule', () => {
  const src = { cleanCommit: () => null, now: () => new Date('2026-01-01T00:00:00.5Z'), randomHex: () => 'abcdef01' };
  assert.equal(presets.resolveBuildId({ ...src, env: 'robin-7' }), 'robin-7');
  assert.throws(() => presets.resolveBuildId({ ...src, env: 'DEV' }), /reserved/);
  assert.throws(() => presets.resolveBuildId({ ...src, env: 'a b' }), /not a valid/);
  assert.equal(presets.resolveBuildId({ ...src, cleanCommit: () => ({ shortSha: 'a1b2c3d', unixTime: 1767225600 }) }), 'a1b2c3d-1767225600');
  assert.equal(presets.resolveBuildId(src), '20260101T000000Z-abcdef01');
});

test('from a clean core/web the three packages build in the §7.3.2 order with no prior step', { timeout: 600_000 }, () => {
  for (const p of pkgs) rmSync(join(root, 'packages', p, 'dist'), { recursive: true, force: true });
  execFileSync('npm', ['run', 'build', ...pkgs.flatMap((p) => ['-w', `@mediacore/${p}`])], { cwd: root, stdio: 'inherit' });
  for (const p of pkgs) assert.ok(existsSync(join(root, 'packages', p, 'dist/index.js')), `${p}/dist/index.js`);
  const version = JSON.parse(readFileSync(join(root, 'packages/shell/package.json'), 'utf8')).version;
  const dist = join(root, 'packages/shell/dist');
  const js = readdirSync(dist, { recursive: true }).filter((f) => String(f).endsWith('.js'));
  assert.ok(
    js.some((f) => readFileSync(join(dist, String(f)), 'utf8').includes(JSON.stringify(version))),
    `shell/dist carries the MEDIACORE_CORE_VERSION literal ${JSON.stringify(version)}`,
  );
  assert.ok(!js.some((f) => readFileSync(join(dist, String(f)), 'utf8').includes('MEDIACORE_CORE_VERSION')), 'no unreplaced define');
});
