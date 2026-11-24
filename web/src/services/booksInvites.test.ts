// file: web/src/services/booksInvites.test.ts
// version: 0.1.0
// guid: cc81aa84-684c-4f7a-9414-4f2808e58324
// last-edited: 2026-11-22

// The books invite list and revoke (F4-08 B10sq): the row keeps no link secret, drops empty
// optional fields, and the revoke goes to the books route by id.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { deleteInvite, listInvites } from './booksInvites';

afterEach(() => vi.unstubAllGlobals());

function stubFetch(handler: (url: string, init?: RequestInit) => Response) {
  const calls: { url: string; method: string }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push({ url, method: init?.method ?? 'GET' });
      return handler(url, init);
    })
  );
  return calls;
}

describe('listInvites', () => {
  it('drops the token from every row, so no link secret reaches component state', async () => {
    stubFetch(
      () =>
        new Response(
          JSON.stringify({
            data: {
              invites: [
                { id: 'inv-1', username: 'selina.kyle', email: 'selina@example.test', role_id: 'viewer', expires_at: 'x', email_sent_at: 'y', token: 'gotham-link-two' },
              ],
              mail_configured: true,
            },
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
    );
    const { invites, mailConfigured } = await listInvites();
    expect(mailConfigured).toBe(true);
    expect(invites).toEqual([
      { id: 'inv-1', username: 'selina.kyle', email: 'selina@example.test', role_id: 'viewer', expires_at: 'x', email_sent_at: 'y' },
    ]);
    expect(JSON.stringify(invites)).not.toContain('gotham-link-two');
    expect(invites[0]).not.toHaveProperty('token');
  });

  it('omits email and email_sent_at when the row has none', async () => {
    stubFetch(
      () =>
        new Response(JSON.stringify({ invites: [{ id: 'inv-2', username: 'harvey.dent', role_id: 'viewer', expires_at: 'x' }] }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
    );
    const { invites, accepted } = await listInvites();
    expect(invites[0]).toEqual({ id: 'inv-2', username: 'harvey.dent', role_id: 'viewer', expires_at: 'x' });
    expect(accepted).toEqual([]);
  });
});

describe('deleteInvite', () => {
  it('deletes by id on the books route', async () => {
    const calls = stubFetch(() => new Response(null, { status: 204 }));
    await deleteInvite('inv 1');
    expect(calls).toEqual([{ url: '/api/v1/users/invites/inv%201', method: 'DELETE' }]);
  });
});
