// file: web/src/utils/apiFetch.build.test.ts
// version: 1.0.0
// guid: f04c676e-4fc8-4f74-a30d-3c938ec0e878
// last-edited: 2026-11-08

// The stale-build rule of apiFetch (07 §7.5.2): the page's build ID is the
// <meta name="mediacore-build"> content, read once when the module loads, so
// each case sets the meta first and imports a fresh module.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type Mod = typeof import('./apiFetch');

async function load(pageId: string | null): Promise<Mod> {
  document.head.querySelectorAll('meta[name="mediacore-build"]').forEach((m) => m.remove());
  if (pageId !== null) {
    const meta = document.createElement('meta');
    meta.setAttribute('name', 'mediacore-build');
    meta.setAttribute('content', pageId);
    document.head.appendChild(meta);
  }
  vi.resetModules();
  return import('./apiFetch');
}

function answer(headers: Record<string, string>, status = 200): Response {
  return new Response('{}', { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

beforeEach(() => {
  vi.unstubAllGlobals();
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.head.querySelectorAll('meta[name="mediacore-build"]').forEach((m) => m.remove());
});

describe('readPageBuildId', () => {
  it('reads the meta content, and "" without the tag', async () => {
    expect((await load('3f9c2ab-1762214400')).readPageBuildId()).toBe('3f9c2ab-1762214400');
    expect((await load(null)).readPageBuildId()).toBe('');
  });
});

describe('checkBuild', () => {
  it('is stale when X-App-Build names another build', async () => {
    const { checkBuild } = await load('old-1');
    expect(checkBuild(answer({ 'X-App-Build': 'new-2' }), { buildId: 'old-1' })).toBe('header-mismatch');
  });

  it('is not stale when the IDs match', async () => {
    const { checkBuild, getStaleReasons } = await load('same-1');
    expect(checkBuild(answer({ 'X-App-Build': 'same-1' }), { buildId: 'same-1' })).toBeNull();
    expect(getStaleReasons()).toEqual([]);
  });

  it.each(['', 'dev'])('never triggers for a page ID of %j', async (pageId) => {
    const { checkBuild, getStaleReasons } = await load(null);
    expect(checkBuild(answer({ 'X-App-Build': 'new-2' }), { buildId: pageId })).toBeNull();
    expect(getStaleReasons()).toEqual([]);
  });

  it.each(['dev', 'DEV'])('treats a header of %j as unknown, never a mismatch', async (header) => {
    const { checkBuild } = await load('old-1');
    expect(checkBuild(answer({ 'X-App-Build': header }), { buildId: 'old-1' })).toBeNull();
  });

  it('flags the app\'s own answer with no X-App-Build (a server before the header)', async () => {
    const { checkBuild } = await load('old-1');
    const csp = { 'Reporting-Endpoints': 'csp="/api/v1/csp-report"' };
    expect(checkBuild(answer(csp), { buildId: 'old-1' })).toBe('pre-r1-server');
    expect(checkBuild(answer(csp, 404), { buildId: 'old-1' })).toBe('pre-r1-server');
  });

  it('does not flag a header-less response that is not the app\'s (a proxy 502, an interstitial)', async () => {
    const { checkBuild } = await load('old-1');
    expect(checkBuild(answer({}, 502), { buildId: 'old-1' })).toBeNull();
    expect(checkBuild(answer({ 'Reporting-Endpoints': 'csp="/r"' }, 502), { buildId: 'old-1' })).toBeNull();
    expect(checkBuild(answer({ 'Reporting-Endpoints': 'default="/r"' }), { buildId: 'old-1' })).toBeNull();
    expect(checkBuild(answer({}), { buildId: 'old-1' })).toBeNull();
  });

  it('ignores a response double with no headers', async () => {
    const { checkBuild } = await load('old-1');
    const bare = { ok: true, status: 200, json: async () => ({}) } as unknown as Response;
    expect(checkBuild(bare, { buildId: 'old-1' })).toBeNull();
  });
});

describe('onStaleBuild', () => {
  it('replays a detection to a listener that subscribes late, once per reason', async () => {
    const { checkBuild, onStaleBuild, getStaleReasons } = await load('old-1');
    const early = vi.fn();
    const stopEarly = onStaleBuild(early);
    checkBuild(answer({ 'X-App-Build': 'new-2' }), { buildId: 'old-1' });
    checkBuild(answer({ 'X-App-Build': 'new-3' }), { buildId: 'old-1' });
    expect(early).toHaveBeenCalledTimes(1);
    expect(early).toHaveBeenCalledWith('header-mismatch');
    expect(getStaleReasons()).toEqual(['header-mismatch']);

    const late = vi.fn();
    const stopLate = onStaleBuild(late);
    expect(late).toHaveBeenCalledTimes(1);
    expect(late).toHaveBeenCalledWith('header-mismatch');

    stopEarly();
    stopLate();
    checkBuild(answer({ 'Reporting-Endpoints': 'csp="/r"' }), { buildId: 'old-1' });
    expect(early).toHaveBeenCalledTimes(1);
    expect(late).toHaveBeenCalledTimes(1);
  });
});

describe('apiFetch applies the rule on every response', () => {
  it.each([
    ['without a timeout', undefined],
    ['with a timeout', 5000],
  ])('%s', async (_name, timeoutMs) => {
    const { apiFetch, getStaleReasons } = await load('old-1');
    vi.stubGlobal('fetch', vi.fn(async () => answer({ 'X-App-Build': 'new-2' })));
    expect(getStaleReasons()).toEqual([]);
    const res = await apiFetch('/api/v1/health', { timeoutMs });
    expect(res.status).toBe(200);
    expect(getStaleReasons()).toEqual(['header-mismatch']);
  });

  it('stays quiet when the page has no build ID', async () => {
    const { apiFetch, getStaleReasons } = await load(null);
    vi.stubGlobal('fetch', vi.fn(async () => answer({ 'X-App-Build': 'new-2' })));
    await apiFetch('/api/v1/health');
    expect(getStaleReasons()).toEqual([]);
  });
});
