// file: core/web/packages/shell/test/buildId.test.ts
// version: 0.1.0
// guid: 175ed22d-124a-48f6-9698-994bf5d17663
// last-edited: 2026-11-20

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ServerModules } from '../src/version/types';

// The api-client keeps its detected reasons at module level, so each test loads
// a fresh copy of it and of the shell's loader.
async function fresh() {
  vi.resetModules();
  const api = await import('@mediacore/api-client');
  const shell = await import('../src/version/modules');
  return { ...api, ...shell };
}

function body(uiBuildId: string): ServerModules {
  return {
    core: { version: '1.0.0' },
    app: { id: 'abo', display_name: 'Wayne Tower', version: '2.0.0', ui_build_id: uiBuildId },
    restart_required: false,
    modules: [],
  };
}
function answer(b: unknown, headers: Record<string, string> = {}, status = 200): Response {
  return new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

describe('loadServerModules', () => {
  const seen: string[] = [];
  beforeEach(() => {
    seen.length = 0;
  });
  afterEach(() => vi.resetModules());

  it('reports a legacy SPA (ui_build_id "" with a non-dev X-App-Build) as legacy-spa, whatever shell value the client holds', async () => {
    const { loadServerModules, onStaleBuild, getStaleReasons } = await fresh();
    onStaleBuild((r) => seen.push(r));
    const fetchFn = vi.fn(async () => answer(body(''), { 'X-App-Build': 'abc123' }));
    const client = { runtime: { fetch: fetchFn as typeof fetch, baseUrl: 'https://cave.example.test', buildId: 'abc123', shell: false } };
    const got = await loadServerModules(client);
    expect(got).toEqual(body(''));
    expect(seen).toEqual(['legacy-spa']);
    expect(getStaleReasons()).toEqual(['legacy-spa']);
  });

  it('accepts the flat client shape and requests the modules path with the abort signal', async () => {
    const { loadServerModules } = await fresh();
    const fetchFn = vi.fn(async () => answer(body('b3f1c2'), { 'X-App-Build': 'b3f1c2' }));
    const ctl = new AbortController();
    await loadServerModules({ fetch: fetchFn as typeof fetch, baseUrl: 'https://cave.example.test', buildId: 'b3f1c2' }, ctl.signal);
    const [url, init] = fetchFn.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('https://cave.example.test/api/core/v1/modules');
    expect(init.signal).toBe(ctl.signal);
  });

  it.each([
    ['a shell UI build with a header', body('b3f1c2'), { 'X-App-Build': 'b3f1c2' }, 'b3f1c2'],
    ['a dev X-App-Build', body(''), { 'X-App-Build': 'dev' }, 'b3f1c2'],
    ['no X-App-Build', body(''), {}, 'b3f1c2'],
    ['a dev page build ID', body(''), { 'X-App-Build': 'abc123' }, 'dev'],
    ['an empty page build ID', body(''), { 'X-App-Build': 'abc123' }, ''],
  ])('does not report %s', async (_name, b, headers, buildId) => {
    const { loadServerModules, getStaleReasons } = await fresh();
    await loadServerModules({ fetch: (async () => answer(b, headers)) as typeof fetch, baseUrl: '', buildId });
    expect(getStaleReasons()).toEqual([]);
  });

  it('also applies the header rule to the response (case 1: header mismatch)', async () => {
    const { loadServerModules, getStaleReasons } = await fresh();
    await loadServerModules({ fetch: (async () => answer(body('zz9'), { 'X-App-Build': 'zz9' })) as typeof fetch, baseUrl: '', buildId: 'abc123' });
    expect(getStaleReasons()).toEqual(['header-mismatch']);
  });

  it('throws a typed error carrying the problem on a non-2xx response', async () => {
    const { loadServerModules, ModulesLoadError } = await fresh();
    const problem = { type: 'about:blank', title: 'Forbidden', status: 403, code: 'permission_denied', detail: 'sign in' };
    const fetchFn = (async () =>
      new Response(JSON.stringify(problem), { status: 403, headers: { 'Content-Type': 'application/problem+json' } })) as typeof fetch;
    const err = await loadServerModules({ fetch: fetchFn, baseUrl: '', buildId: 'abc123' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ModulesLoadError);
    const typed = err as InstanceType<typeof ModulesLoadError>;
    expect(typed.status).toBe(403);
    expect(typed.problem).toMatchObject({ code: 'permission_denied', status: 403 });
  });

  it('does not run the case-2 check on a failed response', async () => {
    const { loadServerModules, getStaleReasons } = await fresh();
    const fetchFn = (async () => answer(body(''), { 'X-App-Build': 'abc123' }, 502)) as typeof fetch;
    await expect(loadServerModules({ fetch: fetchFn, baseUrl: '', buildId: 'abc123' })).rejects.toThrow();
    expect(getStaleReasons()).toEqual([]);
  });
});
