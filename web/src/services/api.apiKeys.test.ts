// file: web/src/services/api.apiKeys.test.ts
// version: 0.1.0
// guid: b0d5e2f5-31d5-42b9-bba8-8f6073c00180
// last-edited: 2026-11-22

// Every API-key call goes to the books route, /api/v1/auth/api-keys, with HEAD's
// body and HEAD's answer shapes (F4-08 B10sk). A key made there carries the books
// ambient scopes (core:self, books:reading, ...), so an audiobook client can sync
// progress with it; a key made on core's native key routes does not.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAPIKey, getAPIKey, listAPIKeys, revokeAPIKey, updateAPIKeyStatus } from './api';

const mockFetch = vi.fn();

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const headKey = (extra: Record<string, unknown> = {}) => ({
  id: 'k1',
  user_id: 'u1',
  name: 'bat-signal',
  scopes: ['library.view'],
  status: 'active',
  created_at: '2026-11-01T00:00:00Z',
  use_count: 0,
  identifier: 'abk_0123abcd',
  days_since_last_use: null,
  never_used: true,
  ...extra,
});

function lastCall(): { url: string; method: string; body: Record<string, unknown> | undefined } {
  const [url, init] = mockFetch.mock.calls[mockFetch.mock.calls.length - 1] as [string, RequestInit | undefined];
  return {
    url: String(url),
    method: init?.method ?? 'GET',
    body: init?.body ? JSON.parse(String(init.body)) : undefined,
  };
}

describe('API keys go to the books key route', () => {
  beforeEach(() => {
    global.fetch = mockFetch as unknown as typeof fetch;
  });
  afterEach(() => mockFetch.mockReset());

  it('create posts HEAD\'s body to /api/v1/auth/api-keys with atom-name scopes and the description', async () => {
    mockFetch.mockResolvedValueOnce(
      json(
        {
          data: {
            id: 'k1',
            name: 'bat-signal',
            token: 'abk_synthetic_token',
            scopes: ['library.view'],
            expires_at: '2027-02-01T00:00:00Z',
            created_at: '2026-11-01T00:00:00Z',
          },
        },
        201
      )
    );
    const made = await createAPIKey({
      name: 'bat-signal',
      description: 'Alfred phone',
      scopes: ['library.view'],
      expires_in_days: 90,
    });
    const call = lastCall();
    expect(call.url).toBe('/api/v1/auth/api-keys');
    expect(call.method).toBe('POST');
    expect(call.body).toEqual({
      name: 'bat-signal',
      description: 'Alfred phone',
      scopes: ['library.view'],
      expires_in_days: 90,
    });
    // HEAD's answer: the one-time token is `token`, in HEAD's data envelope.
    expect(made.token).toBe('abk_synthetic_token');
    expect(made.scopes).toEqual(['library.view']);
  });

  it('create for another user sends user_id in the body of the same route', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: { id: 'k2', name: 'cape', token: 'abk_x', scopes: [], created_at: 'x' } }, 201));
    await createAPIKey({ name: 'cape', scopes: ['library.view'], user_id: 'u-robin', expires_in_days: 30 });
    const call = lastCall();
    expect(call.url).toBe('/api/v1/auth/api-keys');
    expect(call.method).toBe('POST');
    expect(call.body).toMatchObject({ name: 'cape', user_id: 'u-robin', scopes: ['library.view'] });
  });

  it('list reads the caller\'s keys from /api/v1/auth/api-keys and every key with ?all=true', async () => {
    mockFetch
      .mockResolvedValueOnce(json({ data: { api_keys: [headKey({ description: 'Spare for the Batcave' }), headKey({ id: 'k2' })], count: 2 } }))
      .mockResolvedValueOnce(json({ data: { api_keys: [headKey()], count: 1 } }));
    const own = await listAPIKeys();
    expect(lastCall()).toMatchObject({ url: '/api/v1/auth/api-keys', method: 'GET' });
    expect(own).toHaveLength(2);
    expect(own[0].description).toBe('Spare for the Batcave');
    expect(own[0].status).toBe('active');
    await listAPIKeys(true);
    expect(lastCall()).toMatchObject({ url: '/api/v1/auth/api-keys?all=true', method: 'GET' });
  });

  it('get reads one key from /api/v1/auth/api-keys/{id}', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: headKey({ id: 'k9', username: 'robin' }) }));
    const k = await getAPIKey('k9');
    expect(lastCall()).toMatchObject({ url: '/api/v1/auth/api-keys/k9', method: 'GET' });
    expect(k.username).toBe('robin');
  });

  it('patch status sends HEAD\'s inactive/active word to /api/v1/auth/api-keys/{id}', async () => {
    mockFetch.mockResolvedValueOnce(json({ data: headKey({ status: 'inactive' }) }));
    const k = await updateAPIKeyStatus('k1', 'inactive');
    expect(lastCall()).toMatchObject({ url: '/api/v1/auth/api-keys/k1', method: 'PATCH', body: { status: 'inactive' } });
    expect(k.status).toBe('inactive');
  });

  it('delete revokes through DELETE /api/v1/auth/api-keys/{id}', async () => {
    mockFetch.mockResolvedValueOnce(new Response(null, { status: 204 }));
    await revokeAPIKey('k1');
    expect(lastCall()).toMatchObject({ url: '/api/v1/auth/api-keys/k1', method: 'DELETE' });
  });

  it('a refusal reaches the caller as an error carrying the server\'s message and status', async () => {
    mockFetch.mockResolvedValueOnce(json({ error: "cannot grant scope you don't have" }, 403));
    await expect(createAPIKey({ name: 'x', scopes: ['users.manage'] })).rejects.toMatchObject({ status: 403 });
  });

  it('no call leaves the books route: every request URL is under /api/v1/auth/api-keys', async () => {
    mockFetch.mockImplementation(async () => json({ data: { api_keys: [], id: 'k', token: 't', scopes: [] } }));
    await listAPIKeys();
    await listAPIKeys(true);
    await getAPIKey('k');
    await createAPIKey({ name: 'n', scopes: [] });
    await createAPIKey({ name: 'n', scopes: [], user_id: 'u' });
    await updateAPIKeyStatus('k', 'active');
    await revokeAPIKey('k');
    const urls = mockFetch.mock.calls.map((c) => String(c[0]));
    expect(urls).toHaveLength(7);
    for (const url of urls) expect(url).toMatch(/^\/api\/v1\/auth\/api-keys(\/k)?(\?all=true)?$/);
  });
});
