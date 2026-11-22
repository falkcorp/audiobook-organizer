// file: web/src/test/mockCoreApi.test.ts
// version: 0.1.0
// guid: 3c8f1a67-2d54-49be-8e07-b5a4d9c1e730
// last-edited: 2026-11-22

// Unit test for the e2e core API mock (web/tests/e2e/utils/mockCoreApi.ts). It
// lives under src/ because vitest excludes tests/e2e. Three things are pinned:
// the handler's answers, the Playwright wiring (against a fake Page), and that
// the REAL shell boots against the mock: createShell mounted in jsdom with fetch
// routed to the handler makes exactly the boot requests 07 section 7.12 lists and
// none the mock does not implement.

import type { Page, Route } from '@playwright/test';
import { act, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createShell } from '@mediacore/shell';
import {
  CORE_API_PREFIX,
  coreModule,
  coreTaskRun,
  createCoreApiMock,
  installCoreApiMock,
  type CoreApiMockOptions,
  type CoreApiRequest,
} from '../../tests/e2e/utils/mockCoreApi';

// Defined by vitest.config.ts (and by the vite preset in a build) as @mediacore/shell's version.
declare const MEDIACORE_CORE_VERSION: string;

const get = (pathname: string): CoreApiRequest => ({ method: 'GET', pathname });
const post = (pathname: string, body?: unknown): CoreApiRequest => ({ method: 'POST', pathname, body });
const p = (rel: string) => `${CORE_API_PREFIX}${rel}`;
const parse = (body: string) => JSON.parse(body) as Record<string, unknown>;

describe('createCoreApiMock: the handler', () => {
  it('answers the session probe for each auth mode', () => {
    const status = (auth: CoreApiMockOptions['auth']) => parse(createCoreApiMock({ auth }).handle(get(p('/auth/status'))).body);
    expect(status('signed-in')).toEqual({ has_users: true, auth_disabled: false, setup_required: false });
    expect(status('anonymous')).toEqual({ has_users: true, auth_disabled: false, setup_required: false });
    expect(status('setup-required')).toEqual({ has_users: false, auth_disabled: false, setup_required: true });
    expect(status('auth-disabled')).toEqual({ has_users: true, auth_disabled: true, setup_required: false });
  });

  it('serves /me, /modules and the task runs to a signed-in caller and 401s an anonymous one', () => {
    const signedIn = createCoreApiMock({ taskRuns: [coreTaskRun('run-1', 'running')] });
    const me = signedIn.handle(get(p('/me')));
    expect(me.status).toBe(200);
    expect(parse(me.body)).toMatchObject({ user: { username: 'batman' }, permissions: ['core:admin'], method: 'session' });
    expect(signedIn.handle(get(p('/modules'))).status).toBe(200);
    expect((parse(signedIn.handle(get(p('/tasks/runs'))).body).runs as unknown[]).length).toBe(1);
    expect(parse(signedIn.handle(get(p('/tasks/runs/run-1'))).body)).toMatchObject({ id: 'run-1', state: 'running' });
    const missing = signedIn.handle(get(p('/tasks/runs/run-9')));
    expect(missing.status).toBe(404);
    expect(missing.headers['Content-Type']).toBe('application/problem+json');

    const anon = createCoreApiMock({ auth: 'anonymous' });
    for (const rel of ['/me', '/modules', '/tasks/runs', '/settings', '/settings/schema']) {
      const res = anon.handle(get(p(rel)));
      expect(res.status, rel).toBe(401);
      expect(res.headers['Content-Type'], rel).toBe('application/problem+json');
    }
    expect(anon.handle(get(p('/auth/status'))).status).toBe(200);
  });

  it('login signs in, logout signs out, and a login without credentials is refused', () => {
    const mock = createCoreApiMock({ auth: 'anonymous' });
    expect(mock.handle(post(p('/auth/login'), { username: 'batman' })).status).toBe(400);
    expect(mock.authMode()).toBe('anonymous');
    const opened = mock.handle(post(p('/auth/login'), { username: 'batman', password: 'robin' }));
    expect(opened.status).toBe(200);
    expect(parse(opened.body)).toMatchObject({ user: { username: 'batman' } });
    expect(mock.handle(get(p('/me'))).status).toBe(200);
    expect(mock.handle(post(p('/auth/logout'))).status).toBe(204);
    expect(mock.handle(get(p('/me'))).status).toBe(401);
  });

  it('reports the app and module list /modules carries, with the core version of the shell package', () => {
    const mock = createCoreApiMock({
      app: { uiBuildId: 'bat-1' },
      modules: [coreModule('books', '1.0.0', 'Books', { nav: ['library'] })],
    });
    const body = parse(mock.handle(get(p('/modules'))).body) as {
      core: { version: string };
      app: { id: string; ui_build_id: string };
      modules: { id: string; ui: { nav: string[]; pages: string[] } }[];
    };
    expect(body.core.version).toBe(MEDIACORE_CORE_VERSION);
    expect(body.app).toMatchObject({ id: 'abo', ui_build_id: 'bat-1' });
    expect(body.modules).toHaveLength(1);
    expect(body.modules[0].ui).toMatchObject({ nav: ['library'], pages: [] });
  });

  it('stamps X-App-Build, dev by default, and leaves it off for ""', () => {
    expect(createCoreApiMock().handle(get(p('/auth/status'))).headers['X-App-Build']).toBe('dev');
    expect(createCoreApiMock({ appBuild: 'old-7' }).handle(get(p('/me'))).headers['X-App-Build']).toBe('old-7');
    expect('X-App-Build' in createCoreApiMock({ appBuild: '' }).handle(get(p('/me'))).headers).toBe(false);
    const rolledBack = createCoreApiMock({ appBuild: '', headers: { 'Reporting-Endpoints': 'csp="/csp"' } }).handle(get(p('/me')));
    expect(rolledBack.headers).toMatchObject({ 'Reporting-Endpoints': 'csp="/csp"' });
    expect('X-App-Build' in rolledBack.headers).toBe(false);
  });

  it('records diagnostics and echoes a settings patch', () => {
    const mock = createCoreApiMock();
    expect(mock.handle(post(p('/ui/diagnostics'), { kind: 'ui-missing' })).status).toBe(204);
    expect(mock.diagnostics()).toEqual([{ kind: 'ui-missing' }]);
    const patched = mock.handle({ method: 'PATCH', pathname: p('/settings'), body: { set: { 'batcave.lights': true } } });
    expect(parse(patched.body)).toEqual({ generation: 1, applied: ['batcave.lights'] });
  });

  it('answers a route it does not implement 501 and fails assertNoUnhandled by name', () => {
    const mock = createCoreApiMock();
    mock.assertNoUnhandled();
    const res = mock.handle(get(p('/gadgets')));
    expect(res.status).toBe(501);
    expect(parse(res.body)).toMatchObject({ code: 'unmocked' });
    expect(mock.unhandled()).toEqual([{ method: 'GET', path: p('/gadgets'), status: 501 }]);
    expect(() => mock.assertNoUnhandled()).toThrow(/GET \/api\/core\/v1\/gadgets/);
    // A 404 from a handled route is not "unhandled".
    const fresh = createCoreApiMock();
    fresh.handle(get(p('/tasks/runs/none')));
    expect(fresh.unhandled()).toEqual([]);
  });
});

/** A Page with just the calls installCoreApiMock makes. */
function fakePage() {
  const routes: { match: (url: URL) => boolean; handler: (route: Route) => Promise<void> }[] = [];
  const page = {
    addInitScript: vi.fn(async () => {}),
    route: vi.fn(async (match: (url: URL) => boolean, handler: (route: Route) => Promise<void>) => {
      routes.push({ match, handler });
    }),
  };
  return { page: page as unknown as Page, spies: page, routes };
}

function fakeRoute(method: string, url: string, postData: string | null = null) {
  const fulfill = vi.fn(async () => {});
  const route = { request: () => ({ url: () => url, method: () => method, postData: () => postData }), fulfill } as unknown as Route;
  return { route, fulfill };
}

describe('installCoreApiMock: the Playwright wiring', () => {
  it('routes core paths except the event stream, and fulfils what the handler answers', async () => {
    const { page, routes } = fakePage();
    const handle = await installCoreApiMock(page, { auth: 'anonymous' });
    // mockCoreEvents registered its own events guard first; the core route is the last one.
    const core = routes[routes.length - 1];
    const u = (path: string) => new URL(`http://127.0.0.1:8500${path}`);
    expect(core.match(u(p('/me')))).toBe(true);
    expect(core.match(u(p('/tasks/runs/x')))).toBe(true);
    expect(core.match(u(p('/events')))).toBe(false);
    expect(core.match(u('/api/v1/health'))).toBe(false);
    expect(core.match(u('/library'))).toBe(false);

    const { route, fulfill } = fakeRoute('POST', `http://127.0.0.1:8500${p('/auth/login')}`, JSON.stringify({ username: 'batman', password: 'robin' }));
    await core.handler(route);
    expect(fulfill).toHaveBeenCalledWith(expect.objectContaining({ status: 200, headers: expect.objectContaining({ 'X-App-Build': 'dev' }) }));
    expect(handle.authMode()).toBe('signed-in');
    expect(handle.requests()).toEqual([{ method: 'POST', path: p('/auth/login'), status: 200 }]);
    expect(handle.events).not.toBeNull();
  });

  it('installs the event shim by default and leaves /events alone for events: false', async () => {
    const on = fakePage();
    await installCoreApiMock(on.page);
    expect(on.spies.addInitScript).toHaveBeenCalledTimes(1);
    const off = fakePage();
    const handle = await installCoreApiMock(off.page, { events: false });
    expect(off.spies.addInitScript).not.toHaveBeenCalled();
    expect(handle.events).toBeNull();
  });

  it('returns the same handle on a second call and refuses different options', async () => {
    const { page } = fakePage();
    const first = await installCoreApiMock(page, { auth: 'anonymous' });
    expect(await installCoreApiMock(page)).toBe(first);
    await expect(installCoreApiMock(page, { auth: 'signed-in' })).rejects.toThrow(/already has the core API mock/);
  });
});

describe('the shell boots against the mock', () => {
  afterEach(() => {
    document.body.innerHTML = '';
    document.head.querySelectorAll('meta[name^="mediacore-"]').forEach((m) => m.remove());
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('signed in: probes the session, loads /me and /modules, and asks for nothing the mock lacks', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const meta = document.createElement('meta');
    meta.setAttribute('name', 'mediacore-app');
    meta.setAttribute('content', 'abo');
    document.head.appendChild(meta);
    const mock = createCoreApiMock({ modules: [] });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://localhost');
        if (url.pathname === p('/events')) return new Promise<Response>(() => {});
        const res = mock.handle({
          method: init?.method ?? 'GET',
          pathname: url.pathname,
          search: url.search,
          body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
        });
        return new Response(res.status === 204 ? null : res.body, { status: res.status, headers: res.headers });
      }),
    );
    window.history.replaceState(null, '', '/');
    const host = document.createElement('div');
    document.body.appendChild(host);
    const shell = createShell({ app: { id: 'abo', displayName: 'Audiobook Organizer' }, modules: [] });
    act(() => shell.mount(host));
    await waitFor(() => {
      const paths = mock.requests().map((r) => r.path);
      expect(paths).toContain(p('/auth/status'));
      expect(paths).toContain(p('/me'));
      expect(paths).toContain(p('/modules'));
    });
    const order = mock.requests().map((r) => r.path);
    expect(order.indexOf(p('/auth/status'))).toBeLessThan(order.indexOf(p('/me')));
    expect(order.indexOf(p('/me'))).toBeLessThan(order.indexOf(p('/modules')));
    mock.assertNoUnhandled();
    act(() => shell.unmount());
  });
});
