// file: web/src/components/users/usersSignInRequired.test.tsx
// version: 0.1.0
// guid: 9c03ed22-367e-4674-aed7-cfb166fb7d2d
// last-edited: 2026-11-22

// SEC5-8: with authentication off core refuses invites, deactivate, reactivate and reset-password
// for the auth-disabled principal. The page says "Turn on sign-in to do this" with a link, HIDES
// deactivate, reactivate and reset-password, keeps invite create and resend visible but disabled,
// and leaves the invite list and revoke live (F4-08 B10sq); a refusal that arrives anyway maps to
// the same message.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { UsersPage } from './UsersPage';
import { coreJson, coreProblem, stubCoreFetch } from '../../test/coreFetch';
import { SIGN_IN_HELP_URL } from '../../utils/signInRequired';

const envelope = (data: unknown) =>
  new Response(JSON.stringify({ data }), { status: 200, headers: { 'Content-Type': 'application/json' } });

const bruce = { id: 'u1', username: 'bruce.wayne', roles: ['admin'], status: 'active', created_at: '2026-01-01T00:00:00Z' };
const harvey = { id: 'u4', username: 'harvey.dent', roles: ['editor'], status: 'disabled', created_at: '2026-01-04T00:00:00Z' };
const invite = {
  id: 'i1',
  username: 'selina.kyle',
  email: 'selina@example.test',
  role_id: 'viewer',
  expires_at: '2026-12-01T00:00:00Z',
};

function serve(extra: Parameters<typeof stubCoreFetch>[0] = {}) {
  return stubCoreFetch({
    'GET /users': () => coreJson({ items: [bruce, harvey], total: 2 }),
    'GET /roles': () => coreJson({ items: [{ name: 'admin' }], total: 1 }),
    'GET /api/v1/users/invites': () =>
      envelope({ invites: [invite], count: 1, mail_configured: true }),
    ...extra,
  });
}

const renderPage = (authDisabled: boolean) =>
  render(
    <MemoryRouter>
      <UsersPage authDisabled={authDisabled} />
    </MemoryRouter>,
  );

afterEach(() => vi.unstubAllGlobals());

describe('Users page under auth-off (SEC5-8)', () => {
  it('shows the message with a link, disables invite create and resend, and hides deactivate, reactivate and reset-password', async () => {
    const { calls } = serve();
    renderPage(true);
    await screen.findByText('bruce.wayne');
    expect(screen.getByText(/Turn on sign-in to do this/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /turn on sign-in/i })).toHaveAttribute('href', SIGN_IN_HELP_URL);
    expect(screen.getByRole('button', { name: 'Create Invite' })).toBeDisabled();
    expect(screen.getByRole('button', { name: /Resend invitation email/ })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Deactivate' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Reactivate' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Reset Password' })).toBeNull();
    // What is not refused stays: the account list, the footprint drawer and the invite list.
    expect(screen.getByRole('button', { name: /Sign-ins and data for bruce.wayne/ })).toBeEnabled();
    expect(screen.getByText('selina.kyle')).toBeInTheDocument();
    expect(calls.filter((c) => c.method !== 'GET')).toHaveLength(0);
  });

  it('keeps the invite list and revoke live under auth-off: revoke goes to the books route', async () => {
    const { calls } = serve({
      'DELETE /api/v1/users/invites/i1': () => envelope({ deleted: 'i1' }),
    });
    const user = userEvent.setup();
    renderPage(true);
    await screen.findByText('selina.kyle');
    await user.click(screen.getByRole('button', { name: /Revoke invitation for selina.kyle/ }));
    expect(await screen.findByText(/Invitation for selina.kyle revoked/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.path)).toEqual(['/api/v1/users/invites/i1']);
    expect(calls.some((c) => c.path.startsWith('/users/invites'))).toBe(false);
  });

  it('leaves the actions on, with no notice, when auth is on', async () => {
    serve();
    renderPage(false);
    await screen.findByText('bruce.wayne');
    expect(screen.queryByText(/Turn on sign-in to do this/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Create Invite' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Deactivate' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Reactivate' })).toBeEnabled();
    expect(screen.getByRole('button', { name: /Resend invitation email/ })).toBeEnabled();
  });

  it('maps a 403 interactive_required from deactivate to the message, not the raw 403', async () => {
    serve({
      'POST /users/u1/disable': coreProblem(403, 'interactive_required', 'this route needs an interactive session'),
    });
    const user = userEvent.setup();
    renderPage(false);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: 'Deactivate' }));
    expect(await screen.findByText(/Turn on sign-in to do this/)).toBeInTheDocument();
    expect(screen.queryByText(/interactive session/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Create Invite' })).toBeDisabled();
  });

  it('maps a 403 interactive_required from reset-password and reactivate the same way', async () => {
    serve({
      'POST /users/u1/reset-password': coreProblem(403, 'interactive_required', 'raw reset refusal'),
      'POST /users/u4/enable': coreProblem(403, 'interactive_required', 'raw enable refusal'),
    });
    const user = userEvent.setup();
    renderPage(false);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getAllByRole('button', { name: 'Reset Password' })[0]);
    expect(await screen.findByText(/Turn on sign-in to do this/)).toBeInTheDocument();
    expect(screen.queryByText(/raw reset refusal/)).toBeNull();
  });

  it('maps a books resend 403 interactive_required to the message', async () => {
    serve({});
    const inner = globalThis.fetch as unknown as (i: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.includes('/api/v1/users/invites/i1/resend')) {
          return new Response(
            JSON.stringify({ code: 'interactive_required', title: 'interactive session required' }),
            { status: 403, headers: { 'Content-Type': 'application/problem+json' } },
          );
        }
        return inner(input, init);
      }),
    );
    const user = userEvent.setup();
    renderPage(false);
    await screen.findByText('bruce.wayne');
    await user.click(await screen.findByRole('button', { name: /Resend invitation email/ }));
    expect(await screen.findByText(/Turn on sign-in to do this/)).toBeInTheDocument();
    expect(screen.queryByText(/interactive session required/)).toBeNull();
  });

  it('maps a 403 from the invite create to the message and closes the dialog', async () => {
    serve({});
    const inner = globalThis.fetch as unknown as (i: RequestInfo | URL, init?: RequestInit) => Promise<Response>;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith('/api/v1/users/invite') && init?.method === 'POST') {
          return new Response(JSON.stringify({ code: 'interactive_required' }), { status: 403 });
        }
        return inner(input, init);
      }),
    );
    const user = userEvent.setup();
    renderPage(false);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: 'Create Invite' }));
    await user.type(await screen.findByLabelText(/username/i), 'oswald.cobblepot');
    const dialog = await screen.findByRole('dialog');
    await user.click(screen.getAllByRole('button', { name: 'Create Invite' }).find((b) => dialog.contains(b))!);
    await waitFor(() => expect(screen.getByText(/Turn on sign-in to do this/)).toBeInTheDocument());
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('keeps an ordinary 403 (auth on, another code) as the raw error', async () => {
    serve({ 'POST /users/u1/disable': coreProblem(403, 'permission_denied', 'You lack users:write') });
    const user = userEvent.setup();
    renderPage(false);
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: 'Deactivate' }));
    expect(await screen.findByText('You lack users:write')).toBeInTheDocument();
    expect(screen.queryByText(/Turn on sign-in to do this/)).toBeNull();
  });
});
