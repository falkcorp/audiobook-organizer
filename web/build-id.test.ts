// file: web/build-id.test.ts
// version: 1.0.0
// guid: e06d367e-5dd9-4c38-8a54-3b0762672030
// last-edited: 2026-11-08

// The UI build ID rule of 07 §7.6.3, branch by branch, and the plugin that
// writes it into index.html.

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import {
  BUILD_ID_PATTERN,
  mediacoreBuildPlugin,
  resolveBuildId,
  stampBuildId,
  type BuildIdSources,
} from './build-id';

const fixedNow = () => new Date('2026-11-08T03:04:05.678Z');

function sources(over: Partial<BuildIdSources> = {}): BuildIdSources {
  return {
    env: undefined,
    cleanCommit: () => null,
    now: fixedNow,
    randomHex: () => 'a1b2c3d4',
    ...over,
  };
}

describe('resolveBuildId', () => {
  it('uses a non-empty BUILD_ID and never asks git', () => {
    const cleanCommit = vi.fn(() => ({ shortSha: 'abc1234', unixTime: 1 }));
    expect(resolveBuildId(sources({ env: 'rc-1.2_3', cleanCommit }))).toBe('rc-1.2_3');
    expect(cleanCommit).not.toHaveBeenCalled();
  });

  it('accepts the 64-character limit and rejects 65', () => {
    expect(resolveBuildId(sources({ env: 'a'.repeat(64) }))).toHaveLength(64);
    expect(() => resolveBuildId(sources({ env: 'a'.repeat(65) }))).toThrow(/not a valid UI build ID/);
  });

  it.each(['has space', 'semi;colon', 'quo"te', 'new\nline', 'slash/x', 'café'])(
    'fails the build on a BUILD_ID outside the pattern: %j',
    (bad) => {
      expect(BUILD_ID_PATTERN.test(bad)).toBe(false);
      expect(() => resolveBuildId(sources({ env: bad }))).toThrow(/not a valid UI build ID/);
    }
  );

  it.each(['dev', 'DEV', 'Dev', 'dEv'])('fails the build on the reserved ID %s in any case', (dev) => {
    expect(() => resolveBuildId(sources({ env: dev }))).toThrow(/reserved/);
  });

  it('lets an ID that merely contains dev through', () => {
    expect(resolveBuildId(sources({ env: 'dev2' }))).toBe('dev2');
    expect(resolveBuildId(sources({ env: 'pre-dev' }))).toBe('pre-dev');
  });

  it('falls through an empty BUILD_ID to the git rule', () => {
    const clean = () => ({ shortSha: 'abc1234', unixTime: 1762214400 });
    expect(resolveBuildId(sources({ env: '', cleanCommit: clean }))).toBe('abc1234-1762214400');
  });

  it('gives a clean checkout <short sha>-<commit unix time>, the same on every build', () => {
    const clean = () => ({ shortSha: '3f9c2ab', unixTime: 1762214400 });
    const a = resolveBuildId(sources({ cleanCommit: clean }));
    const b = resolveBuildId(sources({ cleanCommit: clean, now: () => new Date(0), randomHex: () => 'ffffffff' }));
    expect(a).toBe('3f9c2ab-1762214400');
    expect(b).toBe(a);
  });

  it('gives a dirty tree or a missing .git <UTC timestamp>-<8 random hex>', () => {
    const id = resolveBuildId(sources());
    expect(id).toBe('20261108T030405Z-a1b2c3d4');
    expect(BUILD_ID_PATTERN.test(id)).toBe(true);
  });

  it('gives two dirty builds of one commit different IDs', () => {
    let n = 0;
    const hexes = ['0f0f0f0f', '1e1e1e1e'];
    const dirty = () => resolveBuildId(sources({ randomHex: () => hexes[n++] }));
    expect(dirty()).not.toBe(dirty());
  });

  it('never produces dev or an ID outside the pattern on any branch', () => {
    for (const id of [
      resolveBuildId(sources({ env: 'x' })),
      resolveBuildId(sources({ cleanCommit: () => ({ shortSha: 'abc1234', unixTime: 5 }) })),
      resolveBuildId(sources()),
    ]) {
      expect(id.toLowerCase()).not.toBe('dev');
      expect(BUILD_ID_PATTERN.test(id)).toBe(true);
    }
  });
});

describe('stampBuildId and the plugin', () => {
  const page = readFileSync(path.resolve(import.meta.dirname, 'index.html'), 'utf8');

  it('index.html carries the dev placeholder the build rewrites', () => {
    expect(page).toMatch(/<meta name="mediacore-build" content="dev" \/>/);
  });

  it('writes the ID into the tag and nowhere else', () => {
    const out = stampBuildId(page, '3f9c2ab-1762214400');
    expect(out).toContain('<meta name="mediacore-build" content="3f9c2ab-1762214400" />');
    expect(out.replace('3f9c2ab-1762214400', 'dev')).toBe(page);
  });

  it('handles the attributes in either order', () => {
    expect(stampBuildId('<meta content="dev" name="mediacore-build">', 'x1')).toBe(
      '<meta content="x1" name="mediacore-build">'
    );
  });

  it('fails when the page has no tag or the tag has no content', () => {
    expect(() => stampBuildId('<html></html>', 'x1')).toThrow(/mediacore-build/);
    expect(() => stampBuildId('<meta name="mediacore-build">', 'x1')).toThrow(/mediacore-build/);
  });

  it('stamps in a build and leaves the page alone under vite serve', () => {
    const build = mediacoreBuildPlugin('batcave-7').transformIndexHtml.handler(page);
    expect(build).toContain('content="batcave-7"');
    const serve = mediacoreBuildPlugin(null).transformIndexHtml.handler(page);
    expect(serve).toBe(page);
    expect(serve).toContain('content="dev"');
  });
});
