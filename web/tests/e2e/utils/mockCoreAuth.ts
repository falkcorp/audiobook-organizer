// file: web/tests/e2e/utils/mockCoreAuth.ts
// version: 0.1.0
// guid: e5a39c71-0d2b-4f68-9b14-7c8e2a6d3f90
// last-edited: 2026-11-21

// e2e mock for core's native identity routes under /api/core/v1 (F4-08), the
// post-switch world the invite and temp-login link specs run in. It answers the
// routes the SPA's fragment pages call (05-authn 4.13): status, me, logout,
// POST /auth/invites/lookup, POST /auth/invites/accept and
// POST /auth/temp-login/consume, with the bare bodies core's authhttp returns
// (no `data` envelope) and the 401 link_invalid a spent, unknown or expired
// token gets.
//
// It also records every request the page makes to /api/ (URL, Referer, body)
// and every history.pushState/replaceState call the page makes, so a spec can
// assert the one-time token never travelled in a URL or a Referer and that the
// SPA removed the fragment with history.replaceState (07 section 7.4.3).
//
// Call installCoreAuthMock(page, ...) before the first page.goto.

import type { Page, Request } from '@playwright/test';

export interface MockInvite {
  token: string;
  username: string;
  role: string;
}

export interface MockTempLogin {
  token: string;
  username: string;
}

export interface CoreAuthMockOptions {
  /** Signed in from the first request, as a user with a session cookie is. */
  signedInAs?: string;
  invites?: MockInvite[];
  tempLogins?: MockTempLogin[];
}

export interface RecordedRequest {
  method: string;
  url: string;
  referer: string | null;
  body: unknown;
}

export interface HistoryCall {
  fn: 'pushState' | 'replaceState';
  url: string;
}

export interface CoreAuthMock {
  /** Every request to a path under /api/, in order. */
  requests: RecordedRequest[];
  /** The page's history.pushState and replaceState calls, in order. */
  historyCalls(): Promise<HistoryCall[]>;
  /** The username the mock currently treats as signed in, or null. */
  signedInAs(): string | null;
}

const NOW = '2026-11-21T12:00:00Z';

function user(username: string, role = 'admin') {
  return {
    id: `user-${username}`,
    username,
    email_verified: false,
    roles: [role],
    status: 'active',
    has_password: true,
    password_reset_required: false,
    sign_in_restricted: false,
    created_at: NOW,
    updated_at: NOW,
  };
}

function problem(status: number, code: string, detail: string) {
  return {
    status,
    contentType: 'application/problem+json',
    body: JSON.stringify({ type: 'about:blank', title: detail, status, code, detail }),
  };
}

function json(body: unknown, status = 200) {
  return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

function bodyOf(request: Request): unknown {
  const raw = request.postData();
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

export async function installCoreAuthMock(page: Page, options: CoreAuthMockOptions = {}): Promise<CoreAuthMock> {
  let current: string | null = options.signedInAs ?? null;
  const invites = new Map((options.invites ?? []).map((i) => [i.token, i]));
  const tempLogins = new Map((options.tempLogins ?? []).map((t) => [t.token, t]));
  const requests: RecordedRequest[] = [];

  // Wrap the two history writers before any page script runs, so the SPA's own
  // calls are recorded and still take effect.
  await page.addInitScript(() => {
    const w = window as unknown as { __historyCalls: { fn: string; url: string }[] };
    w.__historyCalls = [];
    for (const fn of ['pushState', 'replaceState'] as const) {
      const original = history[fn].bind(history);
      history[fn] = (data: unknown, unused: string, url?: string | URL | null) => {
        w.__historyCalls.push({ fn, url: url == null ? '' : String(url) });
        original(data, unused, url);
      };
    }
  });

  page.on('request', (request) => {
    if (!new URL(request.url()).pathname.startsWith('/api/')) return;
    requests.push({
      method: request.method(),
      url: request.url(),
      referer: request.headers()['referer'] ?? null,
      body: bodyOf(request),
    });
  });

  await page.route('**/api/core/v1/**', async (route) => {
    const request = route.request();
    const { pathname } = new URL(request.url());
    const method = request.method();
    const path = pathname.replace(/^\/api\/core\/v1/, '');
    const body = bodyOf(request) as Record<string, unknown> | null;

    if (method === 'GET' && path === '/auth/status') {
      return route.fulfill(json({ has_users: true, auth_disabled: false, setup_required: false }));
    }
    if (method === 'GET' && path === '/me') {
      if (current === null) return route.fulfill(problem(401, 'unauthenticated', 'sign in'));
      return route.fulfill(json({ user: user(current), permissions: ['core:admin'], method: 'session', owner_verified: false }));
    }
    if (method === 'POST' && path === '/auth/logout') {
      current = null;
      return route.fulfill({ status: 204 });
    }
    if (method === 'POST' && path === '/auth/invites/lookup') {
      const invite = invites.get(String(body?.token ?? ''));
      if (!invite) return route.fulfill(problem(401, 'link_invalid', 'this link is not valid'));
      return route.fulfill(json({ username: invite.username, role: invite.role, expires_at: '2026-11-24T12:00:00Z' }));
    }
    if (method === 'POST' && path === '/auth/invites/accept') {
      const token = String(body?.token ?? '');
      const invite = invites.get(token);
      if (!invite || !body?.password) return route.fulfill(problem(401, 'link_invalid', 'this link is not valid'));
      invites.delete(token); // an invite is spent once
      current = invite.username;
      return route.fulfill(json({ public_id: 'session-1', expires_at: '2026-11-22T12:00:00Z', user: user(invite.username, invite.role) }));
    }
    if (method === 'POST' && path === '/auth/temp-login/consume') {
      const token = String(body?.token ?? '');
      const link = tempLogins.get(token);
      if (!link) return route.fulfill(problem(401, 'link_invalid', 'this link is not valid'));
      tempLogins.delete(token); // a temp-login link is spent once
      current = link.username;
      return route.fulfill(json({ public_id: 'session-2', expires_at: '2026-11-22T12:00:00Z', user: user(link.username) }));
    }
    return route.fulfill(problem(404, 'not_found', `the mock has no ${method} ${path}`));
  });

  return {
    requests,
    historyCalls: () =>
      page.evaluate(() => (window as unknown as { __historyCalls: HistoryCall[] }).__historyCalls ?? []),
    signedInAs: () => current,
  };
}
