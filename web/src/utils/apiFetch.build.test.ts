// file: web/src/utils/apiFetch.build.test.ts
// version: 1.0.1
// guid: f04c676e-4fc8-4f74-a30d-3c938ec0e878
// last-edited: 2026-11-08

// apiFetch and the stale-build rule (07 §7.5.2). The rule itself, its three cases
// and its subscription, is @mediacore/api-client's and is tested there
// (core/web/packages/api-client/test/buildId.test.ts). What is ABO's, and tested
// here: the page's build ID is the <meta name="mediacore-build"> content, read once
// when apiFetch loads; apiFetch runs the runtime's one checkBuild on every
// response, with shell false; and a detection reaches a subscriber of either
// module path. Each case sets the meta first and imports a fresh module.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type Mod = typeof import('./apiFetch');
type Runtime = typeof import('@mediacore/api-client');

async function load(pageId: string | null): Promise<{ abo: Mod; runtime: Runtime }> {
  document.head.querySelectorAll('meta[name="mediacore-build"]').forEach((m) => m.remove());
  if (pageId !== null) {
    const meta = document.createElement('meta');
    meta.setAttribute('name', 'mediacore-build');
    meta.setAttribute('content', pageId);
    document.head.appendChild(meta);
  }
  vi.resetModules();
  const abo = await import('./apiFetch');
  const runtime = await import('@mediacore/api-client');
  return { abo, runtime };
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

describe('the one rule', () => {
  it("apiFetch's checkBuild, readPageBuildId, onStaleBuild and getStaleReasons ARE the runtime's", async () => {
    const { abo, runtime } = await load('old-1');
    expect(abo.checkBuild).toBe(runtime.checkBuild);
    expect(abo.readPageBuildId).toBe(runtime.readPageBuildId);
    expect(abo.onStaleBuild).toBe(runtime.onStaleBuild);
    expect(abo.getStaleReasons).toBe(runtime.getStaleReasons);
  });

  it('reads the build ID from the page meta, and "" without the tag', async () => {
    expect((await load('3f9c2ab-1762214400')).abo.readPageBuildId()).toBe('3f9c2ab-1762214400');
    expect((await load(null)).abo.readPageBuildId()).toBe('');
  });
});

describe('apiFetch applies the rule on every response', () => {
  it.each([
    ['without a timeout', undefined],
    ['with a timeout', 5000],
  ])('%s', async (_name, timeoutMs) => {
    const { abo } = await load('old-1');
    vi.stubGlobal('fetch', vi.fn(async () => answer({ 'X-App-Build': 'new-2' })));
    expect(abo.getStaleReasons()).toEqual([]);
    const res = await abo.apiFetch('/api/v1/health', { timeoutMs });
    expect(res.status).toBe(200);
    expect(abo.getStaleReasons()).toEqual(['header-mismatch']);
  });

  it("offers the app's own answer with no X-App-Build (a rollback to R0) a reload", async () => {
    const { abo } = await load('old-1');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => answer({ 'Reporting-Endpoints': 'csp="/api/v1/csp-report"' })),
    );
    await abo.apiFetch('/api/v1/health');
    expect(abo.getStaleReasons()).toEqual(['pre-r1-server']);
  });

  it('notifies a subscriber of the runtime, which is the one the banner hears', async () => {
    const { abo, runtime } = await load('old-1');
    const heard = vi.fn();
    runtime.onStaleBuild(heard);
    vi.stubGlobal('fetch', vi.fn(async () => answer({ 'X-App-Build': 'new-2' })));
    await abo.apiFetch('/api/v1/health');
    expect(heard).toHaveBeenCalledWith('header-mismatch');
  });

  it('stays quiet when the page has no build ID', async () => {
    const { abo } = await load(null);
    vi.stubGlobal('fetch', vi.fn(async () => answer({ 'X-App-Build': 'new-2' })));
    await abo.apiFetch('/api/v1/health');
    expect(abo.getStaleReasons()).toEqual([]);
  });
});
