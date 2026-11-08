// file: core/web/packages/api-client/test/buildId.test.ts
// version: 1.0.0
// guid: 19feb72c-7b17-417f-b2b4-c112eee95244
// last-edited: 2026-11-08

// The stale-build rule of 07 §7.5.2, with buildId and shell passed by the caller,
// never read from a define (FE6-1, FE6-8). The detections live in module state, so
// each case imports a fresh copy of the package.

import { afterEach, describe, expect, it, vi } from 'vitest';

type Mod = typeof import('../src/index');

async function load(): Promise<Mod> {
  vi.resetModules();
  return import('../src/index');
}

function setMeta(content: string | null): void {
  document.head.querySelectorAll('meta[name="mediacore-build"]').forEach((m) => m.remove());
  if (content === null) return;
  const meta = document.createElement('meta');
  meta.setAttribute('name', 'mediacore-build');
  meta.setAttribute('content', content);
  document.head.appendChild(meta);
}

function answer(headers: Record<string, string>, status = 200, body = '{}'): Response {
  return new Response(status === 204 ? null : body, { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

const CSP = { 'Reporting-Endpoints': 'csp="/api/v1/csp-report"' };
const page = (buildId: string, shell = false) => ({ buildId, shell });

afterEach(() => {
  setMeta(null);
  vi.unstubAllGlobals();
});

describe('readPageBuildId', () => {
  it('reads <meta name="mediacore-build"> and "" without the tag', async () => {
    const { readPageBuildId } = await load();
    setMeta('3f9c2ab-1762214400');
    expect(readPageBuildId()).toBe('3f9c2ab-1762214400');
    setMeta('  padded-1  ');
    expect(readPageBuildId()).toBe('padded-1');
    setMeta(null);
    expect(readPageBuildId()).toBe('');
  });
});

describe('checkBuild (cases 1 and 3, headers only)', () => {
  it.each([false, true])('case 1: X-App-Build names another build (shell %s)', async (shell) => {
    const { checkBuild } = await load();
    expect(checkBuild(answer({ 'X-App-Build': 'new-2' }), page('old-1', shell))).toBe('header-mismatch');
  });

  it('is quiet when the IDs match', async () => {
    const { checkBuild, getStaleReasons } = await load();
    expect(checkBuild(answer({ 'X-App-Build': 'same-1' }), page('same-1'))).toBeNull();
    expect(getStaleReasons()).toEqual([]);
  });

  it.each(['', 'dev', 'DEV'])('never triggers for a buildId of %j', async (buildId) => {
    const { checkBuild, getStaleReasons } = await load();
    expect(checkBuild(answer({ 'X-App-Build': 'new-2' }), page(buildId))).toBeNull();
    expect(checkBuild(answer(CSP), page(buildId))).toBeNull();
    expect(getStaleReasons()).toEqual([]);
  });

  it.each(['dev', 'DEV'])('a real buildId against a header of %j is unknown, not a mismatch (FE5-9)', async (header) => {
    const { checkBuild } = await load();
    expect(checkBuild(answer({ 'X-App-Build': header }), page('old-1'))).toBeNull();
  });

  it("case 3: the app's own answer with no X-App-Build (a server before the header)", async () => {
    const { checkBuild } = await load();
    expect(checkBuild(answer(CSP), page('old-1'))).toBe('pre-r1-server');
    expect(checkBuild(answer(CSP, 404), page('old-1'))).toBe('pre-r1-server');
    expect(checkBuild(answer(CSP, 204), page('old-1', true))).toBe('pre-r1-server');
  });

  it("a header-less response that is not the app's is never stale (a proxy 502, an interstitial)", async () => {
    const { checkBuild } = await load();
    expect(checkBuild(answer({}, 502), page('old-1'))).toBeNull();
    expect(checkBuild(answer(CSP, 502), page('old-1'))).toBeNull();
    expect(checkBuild(answer({ 'Reporting-Endpoints': 'default="/r"' }), page('old-1'))).toBeNull();
    expect(checkBuild(answer({}), page('old-1'))).toBeNull();
  });

  it('ignores a response double with no headers', async () => {
    const { checkBuild } = await load();
    const bare = { ok: true, status: 200, json: async () => ({}) } as unknown as Response;
    expect(checkBuild(bare, page('old-1'))).toBeNull();
  });

  it('never reads the body', async () => {
    const { checkBuild } = await load();
    const res = answer({ 'X-App-Build': 'new-2' }, 200, '{"kept":true}');
    checkBuild(res, page('old-1'));
    expect(res.bodyUsed).toBe(false);
    expect(await res.json()).toEqual({ kept: true });
  });
});

describe('checkModules (case 2, the shell only)', () => {
  const legacy = { app: { ui_build_id: '' } };

  it('detects a legacy SPA: ui_build_id "" with a real X-App-Build, from the shell', async () => {
    const { checkModules, getStaleReasons } = await load();
    const res = answer({ 'X-App-Build': 'old-spa-9' });
    expect(checkModules(legacy, res, page('shell-1', true))).toBe('legacy-spa');
    expect(getStaleReasons()).toEqual(['legacy-spa']);
    expect(res.bodyUsed).toBe(false);
  });

  it('is not offered to a pre-shell bundle (FE5-8): it IS the legacy SPA', async () => {
    const { checkModules, getStaleReasons } = await load();
    expect(checkModules(legacy, answer({ 'X-App-Build': 'old-spa-9' }), page('old-1', false))).toBeNull();
    expect(getStaleReasons()).toEqual([]);
  });

  it.each([
    ['a body that names its bundle', { app: { ui_build_id: 'shell-1' } }, { 'X-App-Build': 'shell-1' }],
    ['an empty header', legacy, {}],
    ['a dev header', legacy, { 'X-App-Build': 'dev' }],
  ])('is quiet for %s', async (_name, body, headers) => {
    const { checkModules } = await load();
    expect(checkModules(body, answer(headers), page('shell-1', true))).toBeNull();
  });

  it.each(['', 'dev'])('is skipped for a buildId of %j', async (buildId) => {
    const { checkModules } = await load();
    expect(checkModules(legacy, answer({ 'X-App-Build': 'old-spa-9' }), page(buildId, true))).toBeNull();
  });
});

describe('onStaleBuild', () => {
  it('replays a detection to a late listener, once per reason, and unsubscribes', async () => {
    const { checkBuild, onStaleBuild, getStaleReasons } = await load();
    const early = vi.fn();
    const stopEarly = onStaleBuild(early);
    checkBuild(answer({ 'X-App-Build': 'new-2' }), page('old-1'));
    checkBuild(answer({ 'X-App-Build': 'new-3' }), page('old-1'));
    expect(early).toHaveBeenCalledTimes(1);
    expect(early).toHaveBeenCalledWith('header-mismatch');
    expect(getStaleReasons()).toEqual(['header-mismatch']);

    const late = vi.fn();
    const stopLate = onStaleBuild(late);
    expect(late).toHaveBeenCalledTimes(1);
    expect(late).toHaveBeenCalledWith('header-mismatch');

    stopEarly();
    stopLate();
    checkBuild(answer(CSP), page('old-1'));
    expect(early).toHaveBeenCalledTimes(1);
    expect(late).toHaveBeenCalledTimes(1);
  });
});

describe('one rule for both transports', () => {
  it('a stale response on a native call and one on a direct checkBuild reach the same listener', async () => {
    const { createCoreClient, checkBuild, onStaleBuild } = await load();
    const heard = vi.fn();
    onStaleBuild(heard);

    // The native call: a core client whose fetch answers a newer build.
    const client = createCoreClient({
      ...page('old-1'),
      fetch: async () => answer({ 'X-App-Build': 'new-2' }),
    });
    await (client as unknown as { GET(path: string): Promise<unknown> }).GET('/api/core/v1/batcave/signal');
    expect(heard).toHaveBeenCalledTimes(1);
    expect(heard).toHaveBeenCalledWith('header-mismatch');

    // The legacy transport calls checkBuild itself, a different reason of the same signal.
    checkBuild(answer(CSP), page('old-1'));
    expect(heard).toHaveBeenCalledTimes(2);
    expect(heard).toHaveBeenLastCalledWith('pre-r1-server');
  });

  it('a listener that subscribes after a native stale response is notified once', async () => {
    const { createCoreClient, onStaleBuild } = await load();
    const client = createCoreClient({ ...page('old-1'), fetch: async () => answer({ 'X-App-Build': 'new-2' }) });
    await (client as unknown as { GET(path: string): Promise<unknown> }).GET('/api/core/v1/batcave/signal');
    const late = vi.fn();
    onStaleBuild(late);
    expect(late).toHaveBeenCalledTimes(1);
  });
});
