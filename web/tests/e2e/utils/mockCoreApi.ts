// file: web/tests/e2e/utils/mockCoreApi.ts
// version: 0.1.0
// guid: 5be7c2d0-9a41-4f38-b6e3-1d70a8c4f952
// last-edited: 2026-11-22

// e2e mock for the core HTTP API (/api/core/v1) that the app shell boots on
// (F9-03, 07 section 7.12): the session probe, the module list, the task runs
// and the settings reads. It is OPT-IN per spec: nothing calls it until a spec
// does, so every spec that runs today keeps its behaviour. The legacy
// /api/v1 routes (setupMockApiRoutes in test-helpers.ts) are a different
// layer; a shell-mode spec for a books page uses both.
//
// What a shell page requests on boot, in order (07 section 7.12):
//   GET  /auth/status        the session probe (AuthProvider)
//   GET  /me                 the signed-in user and the caller's permissions
//   GET  /modules            the intersection input (ModulesGate)
//   POST /ui/diagnostics     once per diagnostic kind (the mock records them)
//   GET  /events             the realtime stream: answered by mockCoreEvents,
//                            which this module composes (events option)
// and on demand: POST /auth/login and /auth/logout, GET /tasks/runs and
// /tasks/runs/{id} (the task tracker and the operations indicator), GET and
// PATCH /settings and GET /settings/schema (the shell's Settings sections).
//
// Any other /api/core/v1 path is answered 501 problem+json `unmocked` and
// recorded, so a shell that starts calling a new route fails the spec by name
// instead of rendering an empty page. unhandled() lists them and
// assertNoUnhandled() throws.
//
// The pure part, createCoreApiMock(options).handle(request), does no I/O, so a
// vitest unit can drive the shell against it (src/test/mockCoreApi.test.ts).
// installCoreApiMock(page, options) is the Playwright wiring around it.
//
// X-App-Build defaults to "dev": the client never treats "dev" as a mismatch,
// so a spec that does not test the stale-build rule is never shown the banner.
// A spec that does passes appBuild (the header) and app.uiBuildId (the body).

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Page, Route } from '@playwright/test';
import { mockCoreEvents, type CoreEventsMock, type MockCoreEventsOptions } from './mockCoreEvents';

export const CORE_API_PREFIX = '/api/core/v1';

// ---- wire shapes (the Go DTOs of core/internal/authhttp, modulehttp, taskhttp, confighttp) ----

export interface CoreUser {
  id: string;
  username: string;
  email?: string;
  email_verified: boolean;
  display_name?: string;
  roles: string[];
  status: string;
  has_password: boolean;
  password_reset_required: boolean;
  sign_in_restricted: boolean;
  created_at: string;
  updated_at: string;
}

/** ServerModuleUI of core/web/packages/shell/src/version/types.ts: the ids a module declares per slot. */
export interface CoreModuleUI {
  pages: string[];
  nav: string[];
  settings_panels: string[];
  widgets: string[];
  banners: string[];
  top_bar_items: string[];
  user_page: string[];
  activity_panes: string[];
  setup_steps: string[];
}

export interface CoreModule {
  id: string;
  version: string;
  display_name: string;
  ui: CoreModuleUI;
}

/** tasks.RunPayload (core/tasks/topics.go). */
export interface CoreTaskRun {
  id: string;
  kind: string;
  mode: string;
  state: string;
  held: boolean;
  actor: { user_id?: string; auth_method?: string; system?: string };
  trigger: string;
  created: string;
  started?: string;
  finished?: string;
  done: number;
  total: number;
  [extra: string]: unknown;
}

export type CoreAuthMode = 'signed-in' | 'anonymous' | 'setup-required' | 'auth-disabled';

export interface CoreApiMockOptions {
  /** How the session probe answers first. Default 'signed-in'. login and logout move it. */
  auth?: CoreAuthMode;
  /** Fields of the signed-in user (default: the admin "batman"). */
  user?: Partial<CoreUser>;
  /** The caller's permissions (default ['core:admin']). */
  permissions?: string[];
  /** The app the server says it is. id must equal the page's mediacore-app meta ('abo'). */
  app?: { id?: string; displayName?: string; version?: string; uiBuildId?: string };
  /** The core version /modules reports. Default: @mediacore/shell's own, which the shell accepts. */
  coreVersion?: string;
  /** The modules the server enables. Default none, so only the shell's own pages mount. */
  modules?: CoreModule[];
  /** Runs GET /tasks/runs lists and GET /tasks/runs/{id} reads. */
  taskRuns?: CoreTaskRun[];
  /** The X-App-Build every answer carries. Default 'dev'. '' leaves the header off. */
  appBuild?: string;
  /** More headers on every answer, e.g. `Reporting-Endpoints: csp=...` for the rollback rule. */
  headers?: Record<string, string>;
  /** `false` leaves /events to the spec; an object configures mockCoreEvents. Default: on. */
  events?: boolean | MockCoreEventsOptions;
}

export interface CoreApiRequest {
  method: string;
  /** The URL's pathname, e.g. /api/core/v1/me. */
  pathname: string;
  search?: string;
  /** The parsed JSON body, when there is one. */
  body?: unknown;
}

export interface CoreApiResponse {
  status: number;
  headers: Record<string, string>;
  /** Already serialised; '' for 204. */
  body: string;
}

export interface CoreApiRecord {
  method: string;
  path: string;
  status: number;
}

export interface CoreApiMock {
  handle(request: CoreApiRequest): CoreApiResponse;
  /** Every core request answered, in order (events excluded: mockCoreEvents records those). */
  requests(): CoreApiRecord[];
  /** Requests to a path the mock does not implement. */
  unhandled(): CoreApiRecord[];
  /** Bodies POSTed to /ui/diagnostics. */
  diagnostics(): unknown[];
  /** The mode the probe answers now (login and logout change it). */
  authMode(): CoreAuthMode;
  setAuthMode(mode: CoreAuthMode): void;
  setTaskRuns(runs: CoreTaskRun[]): void;
  assertNoUnhandled(): void;
}

// ---- builders --------------------------------------------------------------

const FIXED_TIME = '2026-11-22T12:00:00Z';

/** The shell package's own version: the one a shell built from this tree reports. */
function shellVersion(): string {
  const here = dirname(fileURLToPath(import.meta.url));
  const manifest = join(here, '../../../../core/web/packages/shell/package.json');
  return (JSON.parse(readFileSync(manifest, 'utf8')) as { version: string }).version;
}

/** A user the mock signs in. Batman fixtures only. */
export function coreUser(over: Partial<CoreUser> = {}): CoreUser {
  return {
    id: 'user-batman',
    username: 'batman',
    email: 'bruce@batcave.example',
    email_verified: true,
    display_name: 'Batman',
    roles: ['admin'],
    status: 'active',
    has_password: true,
    password_reset_required: false,
    sign_in_restricted: false,
    created_at: FIXED_TIME,
    updated_at: FIXED_TIME,
    ...over,
  };
}

/** A module entry with every slot empty unless `ui` fills it. */
export function coreModule(id: string, version: string, displayName: string, ui: Partial<CoreModuleUI> = {}): CoreModule {
  return {
    id,
    version,
    display_name: displayName,
    ui: {
      pages: [],
      nav: [],
      settings_panels: [],
      widgets: [],
      banners: [],
      top_bar_items: [],
      user_page: [],
      activity_panes: [],
      setup_steps: [],
      ...ui,
    },
  };
}

/** A task run in `state`; fixtures are Batman's. */
export function coreTaskRun(id: string, state: string, over: Partial<CoreTaskRun> = {}): CoreTaskRun {
  const finished = ['succeeded', 'failed', 'timed_out', 'canceled', 'interrupted_dropped'].includes(state);
  return {
    id,
    kind: 'batcave.patrol',
    mode: 'run',
    state,
    held: false,
    actor: { user_id: 'user-batman', auth_method: 'session' },
    trigger: 'manual',
    created: FIXED_TIME,
    ...(state === 'queued' ? {} : { started: FIXED_TIME }),
    ...(finished ? { finished: FIXED_TIME } : {}),
    done: finished ? 1 : 0,
    total: 1,
    ...over,
  };
}

function problem(status: number, code: string, title: string, detail = title): string {
  return JSON.stringify({ type: 'about:blank', title, status, code, detail });
}

// ---- the pure handler ------------------------------------------------------

export function createCoreApiMock(options: CoreApiMockOptions = {}): CoreApiMock {
  let auth: CoreAuthMode = options.auth ?? 'signed-in';
  let runs = [...(options.taskRuns ?? [])];
  const appBuild = options.appBuild ?? 'dev';
  const app = {
    id: options.app?.id ?? 'abo',
    display_name: options.app?.displayName ?? 'Audiobook Organizer',
    version: options.app?.version ?? '0.0.0-e2e',
    ui_build_id: options.app?.uiBuildId ?? 'dev',
  };
  const coreVersion = options.coreVersion ?? shellVersion();
  const modules = options.modules ?? [];
  const user = coreUser(options.user);
  const permissions = options.permissions ?? ['core:admin'];
  const requests: CoreApiRecord[] = [];
  const unhandled: CoreApiRecord[] = [];
  const diagnostics: unknown[] = [];

  const baseHeaders = (): Record<string, string> => ({ ...(appBuild === '' ? {} : { 'X-App-Build': appBuild }), ...options.headers });
  const json = (status: number, body: unknown): CoreApiResponse => ({
    status,
    headers: { ...baseHeaders(), 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const failure = (status: number, code: string, title: string, detail?: string): CoreApiResponse => ({
    status,
    headers: { ...baseHeaders(), 'Content-Type': 'application/problem+json' },
    body: problem(status, code, title, detail),
  });
  const noContent = (): CoreApiResponse => ({ status: 204, headers: baseHeaders(), body: '' });

  function route(req: CoreApiRequest): CoreApiResponse | null {
    const rel = req.pathname.slice(CORE_API_PREFIX.length);
    const { method } = req;
    const signedIn = auth === 'signed-in' || auth === 'auth-disabled';

    if (method === 'GET' && rel === '/auth/status') {
      return json(200, {
        has_users: auth !== 'setup-required',
        auth_disabled: auth === 'auth-disabled',
        setup_required: auth === 'setup-required',
      });
    }
    if (method === 'GET' && rel === '/auth/providers') return json(200, { providers: [] });
    if (method === 'GET' && rel === '/me') {
      if (!signedIn) return failure(401, 'unauthenticated', 'Unauthorized', 'sign in first');
      return json(200, { user, permissions, method: 'session', owner_verified: true });
    }
    if (method === 'POST' && rel === '/auth/login') {
      const body = (req.body ?? {}) as { username?: string; password?: string };
      if (!body.username || !body.password) return failure(400, 'invalid_request', 'Bad Request', 'username and password are required');
      auth = 'signed-in';
      return json(200, { public_id: 'session-batcave', expires_at: '2026-12-22T12:00:00Z', user });
    }
    if (method === 'POST' && rel === '/auth/logout') {
      auth = 'anonymous';
      return noContent();
    }
    if (method === 'GET' && rel === '/modules') {
      if (!signedIn) return failure(401, 'unauthenticated', 'Unauthorized', 'sign in first');
      return json(200, { core: { version: coreVersion }, app, restart_required: false, modules });
    }
    if (method === 'POST' && rel === '/ui/diagnostics') {
      diagnostics.push(req.body ?? null);
      return noContent();
    }
    if (method === 'GET' && rel === '/tasks/runs') {
      if (!signedIn) return failure(401, 'unauthenticated', 'Unauthorized', 'sign in first');
      return json(200, { runs });
    }
    const one = /^\/tasks\/runs\/([^/]+)$/.exec(rel);
    if (method === 'GET' && one) {
      if (!signedIn) return failure(401, 'unauthenticated', 'Unauthorized', 'sign in first');
      const id = decodeURIComponent(one[1]);
      const run = runs.find((r) => r.id === id);
      return run ? json(200, run) : failure(404, 'not_found', 'Not Found', `no run ${id}`);
    }
    if (method === 'GET' && rel === '/settings/schema') {
      if (!signedIn) return failure(401, 'unauthenticated', 'Unauthorized', 'sign in first');
      return json(200, { type: 'object', properties: {} });
    }
    if (method === 'GET' && rel === '/settings') {
      if (!signedIn) return failure(401, 'unauthenticated', 'Unauthorized', 'sign in first');
      return json(200, {});
    }
    if (method === 'PATCH' && rel === '/settings') {
      if (!signedIn) return failure(401, 'unauthenticated', 'Unauthorized', 'sign in first');
      const set = ((req.body ?? {}) as { set?: Record<string, unknown> }).set ?? {};
      return json(200, { generation: 1, applied: Object.keys(set) });
    }
    return null;
  }

  return {
    handle(req) {
      const res = route(req) ?? failure(501, 'unmocked', 'Not Implemented', `${req.method} ${req.pathname} is not implemented by mockCoreApi`);
      const record = { method: req.method, path: req.pathname, status: res.status };
      requests.push(record);
      if (res.status === 501 && res.body.includes('"unmocked"')) unhandled.push(record);
      return res;
    },
    requests: () => [...requests],
    unhandled: () => [...unhandled],
    diagnostics: () => [...diagnostics],
    authMode: () => auth,
    setAuthMode(mode) {
      auth = mode;
    },
    setTaskRuns(next) {
      runs = [...next];
    },
    assertNoUnhandled() {
      if (unhandled.length === 0) return;
      const list = unhandled.map((r) => `${r.method} ${r.path}`).join(', ');
      throw new Error(`mockCoreApi: ${unhandled.length} core request(s) it does not implement: ${list}`);
    },
  };
}

// ---- Playwright wiring -----------------------------------------------------

export interface CoreApiHandle extends CoreApiMock {
  /** The event-stream handle when events was left on (the default), else null. */
  events: CoreEventsMock | null;
}

/** One entry per Page, as mockCoreEvents does: a second call returns the same handle. */
const installed = new WeakMap<Page, Promise<CoreApiHandle>>();

const isCoreApi = (url: URL) => url.pathname.startsWith(`${CORE_API_PREFIX}/`) && url.pathname !== `${CORE_API_PREFIX}/events`;

function parseBody(raw: string | null): unknown {
  if (!raw) return undefined;
  try {
    return JSON.parse(raw);
  } catch {
    return undefined;
  }
}

/**
 * Registers the core API mock on `page`. Call it before the first page.goto.
 * Returns the handle the spec asserts on. `/api/core/v1/events` is left to
 * mockCoreEvents, which this call installs unless options.events is false.
 */
export function installCoreApiMock(page: Page, options: CoreApiMockOptions = {}): Promise<CoreApiHandle> {
  const prior = installed.get(page);
  if (prior) {
    if (Object.keys(options).length > 0) {
      return Promise.reject(new Error('installCoreApiMock: this page already has the core API mock; configure it in the first call'));
    }
    return prior;
  }
  const handle = setUp(page, options);
  installed.set(page, handle);
  return handle;
}

async function setUp(page: Page, options: CoreApiMockOptions): Promise<CoreApiHandle> {
  const mock = createCoreApiMock(options);
  const events =
    options.events === false ? null : await mockCoreEvents(page, typeof options.events === 'object' ? options.events : {});
  await page.route(isCoreApi, async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const res = mock.handle({
      method: request.method(),
      pathname: url.pathname,
      search: url.search,
      body: parseBody(request.postData()),
    });
    await route.fulfill({ status: res.status, headers: res.headers, body: res.body });
  });
  return { ...mock, events };
}
