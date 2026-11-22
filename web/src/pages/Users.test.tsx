// file: web/src/pages/Users.test.tsx
// version: 1.6.0
// guid: 26b39541-b8d3-4fc8-b131-190f15f5a5ab
// last-edited: 2026-10-21

// The Users page on core's native identity routes (F4-08 B8): the list, disable and enable,
// the merged-away marker, the one-time reset and invite links, invite revocation, and the one
// call that stays on books (set email).

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { coreJson, coreProblem, stubCoreFetch, type CoreHandler } from '../test/coreFetch';
import { loginPageResponse } from '../test/loginRedirect';
import Users from './Users';

const activeUser = {
  id: 'u1',
  username: 'bruce.wayne',
  email: '',
  roles: ['editor'],
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
};

const page = (items: unknown[]) => coreJson({ items, total: items.length });

function serve(
  users: unknown[],
  extra: Record<string, CoreHandler | Response | (() => Response)> = {},
  invites: unknown[] = []
) {
  return stubCoreFetch({
    'GET /users': () => page(users),
    'GET /users/invites': () => page(invites),
    'GET /roles': page([{ name: 'admin' }, { name: 'editor' }, { name: 'viewer' }]),
    ...extra,
  });
}

const listLoads = (calls: { method: string; path: string }[]) =>
  calls.filter((c) => c.method === 'GET' && c.path.startsWith('/users?')).length;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('Users', () => {
  it('renders the accounts core lists', async () => {
    serve([activeUser]);
    renderWithProviders(<Users />);
    expect(await screen.findByText('bruce.wayne')).toBeInTheDocument();
  });

  it('shows the problem core answered with, not an empty list, when the list fails', async () => {
    serve([], { 'GET /users': coreProblem(500, 'internal', 'the store is unavailable') });
    renderWithProviders(<Users />);
    expect(await screen.findByText('the store is unavailable')).toBeInTheDocument();
  });

  it('shows an error, not an empty list, when the session has expired on load', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => loginPageResponse())
    );
    renderWithProviders(<Users />);
    // The extension panels bounce too and show their own alert; the page's own names core's list.
    expect(await screen.findByText(/session expired\):.*\/api\/core\/v1\/users\?limit=1000/i)).toBeInTheDocument();
  });

  it('shows an expired-session error when a disable is answered with a login page', async () => {
    serve([activeUser], { 'POST /users/u1/disable': () => loginPageResponse() });
    const user = userEvent.setup();
    renderWithProviders(<Users />);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));
    expect(await screen.findByText(/session expired\):.*\/users\/u1\/disable/i)).toBeInTheDocument();
  });

  it('surfaces a refused disable instead of silently reloading', async () => {
    const { calls } = serve([activeUser], {
      'POST /users/u1/disable': coreProblem(409, 'last_admin', 'cannot disable the last admin'),
    });
    const user = userEvent.setup();
    renderWithProviders(<Users />);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));
    expect(await screen.findByText('cannot disable the last admin')).toBeInTheDocument();
    expect(listLoads(calls)).toBe(1);
  });

  it('reloads the list after a successful disable', async () => {
    const { calls } = serve([activeUser], {
      'POST /users/u1/disable': () => coreJson(activeUser),
    });
    const user = userEvent.setup();
    renderWithProviders(<Users />);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));
    await waitFor(() => expect(listLoads(calls)).toBe(2));
    // The extension panels (books' merges and phone keys) have no stub here and may show their own
    // alerts; the page's own failure messages must be absent.
    expect(screen.queryByText(/failed to (deactivate|load users)/i)).not.toBeInTheDocument();
  });

  it('enables a disabled account', async () => {
    const { calls } = serve([{ ...activeUser, status: 'disabled' }], {
      'POST /users/u1/enable': () => coreJson(activeUser),
    });
    const user = userEvent.setup();
    renderWithProviders(<Users />);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: /reactivate/i }));
    await waitFor(() => expect(calls.some((c) => c.path === '/users/u1/enable')).toBe(true));
  });

  it('surfaces a failed enable', async () => {
    serve([{ ...activeUser, status: 'disabled' }], {
      'POST /users/u1/enable': coreProblem(500, 'internal', 'the store is unavailable'),
    });
    const user = userEvent.setup();
    renderWithProviders(<Users />);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: /reactivate/i }));
    expect(await screen.findByText('the store is unavailable')).toBeInTheDocument();
  });

  it('shows a merged-away account as merged and offers no Enable for it', async () => {
    const { calls } = serve([
      { ...activeUser, status: 'disabled', merged_into_user_id: 'u2' },
      { ...activeUser, id: 'u2', username: 'harvey.dent', status: 'disabled' },
    ]);
    renderWithProviders(<Users />);
    await screen.findByText('bruce.wayne');
    expect(screen.getByText('merged')).toBeInTheDocument();
    // Only the account that was not merged away can be enabled.
    expect(screen.getAllByRole('button', { name: /reactivate/i })).toHaveLength(1);
    expect(calls.some((c) => c.path.endsWith('/enable'))).toBe(false);
  });

  it('copies the one-time reset link as an absolute URL and says it is shown once', async () => {
    serve([activeUser], {
      'POST /users/u1/reset-password': () => coreJson({ link: '/login/temp#token=mcl_reset_0001' }),
    });
    const user = userEvent.setup();
    // userEvent.setup installs its own clipboard; spy on it after that.
    const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined);
    renderWithProviders(<Users />);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: /reset password/i }));
    const expected = `${window.location.origin}/login/temp#token=mcl_reset_0001`;
    await waitFor(() => expect(write).toHaveBeenCalledWith(expected));
    expect(await screen.findByText(/shown once/i)).toBeInTheDocument();
  });

  describe('set email', () => {
    const withEmail = { ...activeUser, email: 'old@example.test' };
    const books = (h: CoreHandler) => ({ 'PUT /api/v1/users/u1/email': h });

    it('PUTs the address to books and reloads with a notice', async () => {
      const { calls } = serve(
        [withEmail],
        books(() => coreJson({ data: { user: { ...withEmail, email: 'new@example.test' } } }))
      );
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('bruce.wayne');
      await user.click(screen.getByRole('button', { name: /set email for bruce.wayne/i }));
      const field = screen.getByLabelText(/^email/i);
      await user.clear(field);
      await user.type(field, 'new@example.test');
      await user.click(screen.getByRole('button', { name: /^save$/i }));
      expect(await screen.findByText(/set to new@example.test/i)).toBeInTheDocument();
      const put = calls.find((c) => c.method === 'PUT');
      expect(put?.body).toEqual({ email: 'new@example.test' });
      expect(listLoads(calls)).toBe(2);
    });

    it('clears the address with an empty string', async () => {
      const { calls } = serve([withEmail], books(() => coreJson({ data: {} })));
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('bruce.wayne');
      await user.click(screen.getByRole('button', { name: /set email for bruce.wayne/i }));
      await user.click(screen.getByRole('button', { name: /clear email/i }));
      expect(await screen.findByText(/cleared/i)).toBeInTheDocument();
      expect(calls.find((c) => c.method === 'PUT')?.body).toEqual({ email: '' });
    });

    it('keeps the dialog open and shows the server refusal', async () => {
      serve(
        [withEmail],
        books(
          () =>
            new Response(JSON.stringify({ error: "setting another user's email needs the admin role" }), {
              status: 403,
              headers: { 'Content-Type': 'application/json' },
            })
        )
      );
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('bruce.wayne');
      await user.click(screen.getByRole('button', { name: /set email for bruce.wayne/i }));
      await user.click(screen.getByRole('button', { name: /^save$/i }));
      expect(await screen.findByText(/needs the admin role/i)).toBeInTheDocument();
      expect(screen.getByRole('dialog')).toBeInTheDocument();
    });
  });

  describe('invitations', () => {
    const pending = {
      id: 'inv-1',
      username: 'pending-guest',
      email: 'pending@example.test',
      role: 'viewer',
      created_by: 'u1',
      created_at: '2026-11-20T12:00:00Z',
      expires_at: '2026-11-27T12:00:00Z',
    };

    const booksInvites = (extra: object = {}) => ({
      'GET /api/v1/users/invites': () =>
        coreJson({ data: { invites: [], mail_configured: true, guest_policy_sync_configured: true, ...extra } }),
    });

    it('lists core pending invitations with no link column', async () => {
      serve([activeUser], booksInvites(), [pending]);
      renderWithProviders(<Users />);
      expect(await screen.findByText('pending-guest')).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /copy invitation link/i })).not.toBeInTheDocument();
    });

    it('creates an invite on books (which sends the email) and shows the fragment link', async () => {
      const { calls } = serve([activeUser], {
        ...booksInvites(),
        'POST /api/v1/users/invite': () =>
          coreJson({ data: { token: 'mcl_invite_0001', email_sent: false } }, 201),
      });
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('bruce.wayne');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      await user.type(screen.getByLabelText(/^username/i), 'pending-guest');
      await user.click(screen.getByRole('button', { name: /^create invite$/i }));
      expect(
        await screen.findByText(`${window.location.origin}/invite#token=mcl_invite_0001`)
      ).toBeInTheDocument();
      const post = calls.find((c) => c.method === 'POST' && c.path.endsWith('/users/invite'));
      expect(post?.body).toMatchObject({ username: 'pending-guest', role_id: 'editor', send_email: false });
    });

    it('says why invitation email is unavailable and disables the send checkbox', async () => {
      serve([activeUser], {
        ...booksInvites({
          mail_configured: false,
          mail_unavailable_reason: "the server's public address is not set (PUBLIC_BASE_URL)",
        }),
      });
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('bruce.wayne');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      expect(await screen.findByText(/PUBLIC_BASE_URL/)).toBeInTheDocument();
      expect(screen.getByRole('checkbox')).toBeDisabled();
    });

    it('creates an emailed invite with the email and send flag, and shows the result', async () => {
      const { calls } = serve([activeUser], {
        ...booksInvites(),
        'POST /api/v1/users/invite': () =>
          coreJson({ data: { token: 'mcl_invite_0002', email_sent: true } }, 201),
      });
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('bruce.wayne');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      await user.type(screen.getByLabelText(/^username/i), 'newguest');
      await user.type(screen.getByLabelText(/^email/i), 'new@example.test');
      await user.click(screen.getAllByRole('button', { name: /create invite/i }).at(-1)!);
      expect(await screen.findByText(/invitation emailed to new@example.test/i)).toBeInTheDocument();
      const post = calls.find((c) => c.method === 'POST' && c.path.endsWith('/users/invite'));
      expect(post?.body).toEqual({
        username: 'newguest',
        role_id: 'editor',
        email: 'new@example.test',
        send_email: true,
      });
    });

    it('falls back to the link and shows why when the email was not sent', async () => {
      serve([activeUser], {
        ...booksInvites(),
        'POST /api/v1/users/invite': () =>
          coreJson({ data: { token: 'mcl_invite_0003', email_sent: false, email_error: 'sending failed: 550' } }, 201),
      });
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('bruce.wayne');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      await user.type(screen.getByLabelText(/^username/i), 'g');
      await user.type(screen.getByLabelText(/^email/i), 'g@example.test');
      await user.click(screen.getAllByRole('button', { name: /create invite/i }).at(-1)!);
      expect(await screen.findByText(/the email was not sent: sending failed: 550/i)).toBeInTheDocument();
      expect(screen.getByText(/send this link to g yourself/i)).toBeInTheDocument();
      expect(screen.getByText(`${window.location.origin}/invite#token=mcl_invite_0003`)).toBeInTheDocument();
    });

    it('warns in the pending list and the create dialog when guest policy sync is off, and not when it is on', async () => {
      serve([activeUser], booksInvites({ guest_policy_sync_configured: false }), [pending]);
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('pending-guest');
      await waitFor(() =>
        expect(screen.getAllByText(/Cloudflare guest access is not configured/)).toHaveLength(1)
      );
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      await waitFor(() =>
        expect(screen.getAllByText(/Cloudflare guest access is not configured/)).toHaveLength(2)
      );
      expect(screen.getAllByText(/CF_GUESTS_POLICY_ID/).length).toBeGreaterThan(0);
    });

    it('shows no guest-access warning when guest policy sync is configured', async () => {
      serve([activeUser], booksInvites({ guest_policy_sync_configured: true }), [pending]);
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('pending-guest');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      await screen.findByLabelText(/^username/i);
      expect(screen.queryByText(/Cloudflare guest access is not configured/)).not.toBeInTheDocument();
    });

    it('resends a pending invitation through books', async () => {
      const { calls } = serve(
        [activeUser],
        {
          ...booksInvites(),
          'POST /api/v1/users/invites/inv-1/resend': () => coreJson({ data: {} }),
        },
        [pending]
      );
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await user.click(await screen.findByRole('button', { name: /resend invitation email to pending@example.test/i }));
      expect(await screen.findByText(/emailed again to pending@example.test/i)).toBeInTheDocument();
      expect(calls.some((c) => c.path.endsWith('/invites/inv-1/resend'))).toBe(true);
    });

    it('shows accepted invites with their Cloudflare outcome', async () => {
      serve(
        [activeUser],
        booksInvites({
          accepted: [
            {
              username: 'alfred.pennyworth',
              email: 'alfred@example.test',
              accepted_at: '2026-11-20T12:00:00Z',
              guest_policy_status: 'added',
            },
          ],
        })
      );
      renderWithProviders(<Users />);
      expect(await screen.findByText('alfred.pennyworth')).toBeInTheDocument();
      expect(screen.getByText(/added to cloudflare guest policy/i)).toBeInTheDocument();
    });

    it('revokes a pending invitation and reloads', async () => {
      const { calls } = serve([activeUser], {
        'DELETE /users/invites/inv-1': () => new Response(null, { status: 204 }),
      }, [pending]);
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await user.click(await screen.findByRole('button', { name: /revoke invitation for pending-guest/i }));
      expect(await screen.findByText(/invitation for pending-guest revoked/i)).toBeInTheDocument();
      expect(calls.some((c) => c.method === 'DELETE' && c.path === '/users/invites/inv-1')).toBe(true);
    });
  });
});
