// file: web/src/pages/UserMerge.test.tsx
// version: 1.0.1
// guid: 7f3a9c15-2d68-4e04-b1a7-96c0e5d83f41
// last-edited: 2026-10-23

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { UserMergeDialog, type MergeableUser } from './UserMerge';
import type { UserMergePlan } from '../services/api';

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const absorbed: MergeableUser = { id: 'u-gone', username: 'first-account', status: 'active' };
const candidates: MergeableUser[] = [
  { id: 'u-stay', username: 'second-account', status: 'active', roles: ['admin'] },
];

const account = (id: string, username: string) => ({
  id,
  username,
  status: 'active',
  roles: ['admin'],
  has_password: true,
});

const makePlan = (over: Partial<UserMergePlan> = {}): UserMergePlan => ({
  survivor_id: 'u-stay',
  absorbed_id: 'u-gone',
  survivor: account('u-stay', 'second-account'),
  absorbed: account('u-gone', 'first-account'),
  counts: {
    book_state: { rows: 12, conflicts: 2 },
    bookmarks: { rows: 3, conflicts: 0 },
  },
  conflicts: [],
  conflict_total: 2,
  ops: 40,
  survivor_identities: [],
  absorbed_identities: [
    {
      id: 'i1',
      provider: 'oidc',
      subject: 'ab…yz',
      email: 'someone@example.com',
      linked_at: '2026-01-01T00:00:00Z',
      owner_email: false,
    },
  ],
  owner_identity_holder: 'survivor',
  owner_identity_on_absorbed: false,
  owner_identity_ack_required: false,
  roles_gained: [],
  email_outcome: '',
  warnings: [],
  refusals: [],
  digest: 'digest-aaaaaaaaaaaa',
  ...over,
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function pickSurvivor(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('combobox', { name: /account that stays/i }));
  await user.click(await screen.findByRole('option', { name: 'second-account' }));
}

describe('UserMergeDialog', () => {
  it('previews the merge and starts it with the digest that was previewed', async () => {
    const calls: Array<{ url: string; body?: string }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url: String(url), body: init?.body as string | undefined });
        if (String(url).endsWith('/preview')) return jsonResponse({ data: makePlan() });
        return jsonResponse({ data: { operation_id: 'op-7', action: 'apply' } }, 202);
      })
    );
    const onStarted = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <UserMergeDialog
        absorbed={absorbed}
        candidates={candidates}
        onClose={() => {}}
        onStarted={onStarted}
      />
    );

    const merge = screen.getByRole('button', { name: /^merge$/i });
    expect(merge).toBeDisabled();
    await pickSurvivor(user);

    expect(await screen.findByText('Books with listening progress')).toBeInTheDocument();
    expect(calls[0].url).toBe('/api/v1/users/u-gone/merge-into/u-stay/preview');
    await user.click(await screen.findByRole('button', { name: /merge into second-account/i }));

    await waitFor(() => expect(onStarted).toHaveBeenCalledWith('op-7', absorbed));
    const apply = calls.find((c) => c.url.endsWith('/apply'));
    expect(apply?.url).toBe('/api/v1/users/u-gone/merge-into/u-stay/apply');
    expect(JSON.parse(apply?.body ?? '{}')).toEqual({
      digest: 'digest-aaaaaaaaaaaa',
      acknowledge_owner_identity: false,
    });
  });

  it('needs an explicit acknowledgement when the account merged away holds the owner sign-in', async () => {
    const calls: Array<{ url: string; body?: string }> = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url: String(url), body: init?.body as string | undefined });
        if (String(url).endsWith('/preview'))
          return jsonResponse({
            data: makePlan({ owner_identity_holder: 'absorbed', owner_identity_on_absorbed: true, owner_identity_ack_required: true }),
          });
        return jsonResponse({ data: { operation_id: 'op-8' } }, 202);
      })
    );
    const onStarted = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <UserMergeDialog
        absorbed={absorbed}
        candidates={candidates}
        onClose={() => {}}
        onStarted={onStarted}
      />
    );
    await pickSurvivor(user);

    const merge = await screen.findByRole('button', { name: /merge into second-account/i });
    expect(merge).toBeDisabled();
    await user.click(screen.getByRole('checkbox', { name: /owner.s sign-in moves/i }));
    expect(merge).toBeEnabled();
    await user.click(merge);

    await waitFor(() => expect(onStarted).toHaveBeenCalled());
    const apply = calls.find((c) => c.url.endsWith('/apply'));
    expect(JSON.parse(apply?.body ?? '{}').acknowledge_owner_identity).toBe(true);
  });

  it('refuses to start while the preview lists a refusal', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (String(url).endsWith('/preview'))
        return jsonResponse({
          data: makePlan({ refusals: ['the surviving account is not an admin'] }),
        });
      return jsonResponse({ data: {} }, 202);
    });
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();
    renderWithProviders(
      <UserMergeDialog
        absorbed={absorbed}
        candidates={candidates}
        onClose={() => {}}
        onStarted={vi.fn()}
      />
    );
    await pickSurvivor(user);

    expect(await screen.findByText('the surviving account is not an admin')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /merge into second-account/i })).toBeDisabled();
    expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith('/apply'))).toBe(false);
  });

  it('shows the fresh preview and does not start when the accounts changed since it was read', async () => {
    const stale = makePlan();
    const fresh = makePlan({ digest: 'digest-bbbbbbbbbbbb', ops: 41 });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (String(url).endsWith('/preview')) return jsonResponse({ data: stale });
        return jsonResponse(
          {
            error: 'the accounts changed since the preview; review the new preview',
            code: 'conflict',
            status: 409,
            plan: fresh,
          },
          409
        );
      })
    );
    const onStarted = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(
      <UserMergeDialog
        absorbed={absorbed}
        candidates={candidates}
        onClose={() => {}}
        onStarted={onStarted}
      />
    );
    await pickSurvivor(user);
    await user.click(await screen.findByRole('button', { name: /merge into second-account/i }));

    expect(await screen.findByText(/accounts changed since the preview/i)).toBeInTheDocument();
    expect(screen.getByText(/41 writes/)).toBeInTheDocument();
    expect(screen.getByText(/digest-bbbbb/)).toBeInTheDocument();
    expect(onStarted).not.toHaveBeenCalled();
  });
});
