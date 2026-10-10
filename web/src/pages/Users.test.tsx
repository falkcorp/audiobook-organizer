// file: web/src/pages/Users.test.tsx
// version: 1.1.0
// guid: 26b39541-b8d3-4fc8-b131-190f15f5a5ab
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { loginPageResponse } from '../test/loginRedirect';
import Users from './Users';

// The server wraps every success as { data: ... } (httputil.SuccessResponse) and
// every failure as { error, code, status }; the helpers below use those shapes.
const envelope = (data: unknown) => jsonResponse({ data });

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const activeUser = {
  id: 'u1',
  username: 'test-user',
  role_id: 'editor',
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('Users', () => {
  it('shows an error, not an empty list, when the session has expired on load', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    renderWithProviders(<Users />);
    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
  });

  it('surfaces a failed deactivate instead of silently reloading', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.endsWith('/deactivate')) return jsonResponse({ error: 'cannot deactivate last admin', status: 409 }, 409);
      if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
      return envelope({ users: [activeUser], count: 1 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));

    expect(await screen.findByText('cannot deactivate last admin')).toBeInTheDocument();
    // No reload after the failed write: one /users fetch from the initial load only.
    const userListCalls = fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/users'));
    expect(userListCalls).toHaveLength(1);
  });

  it('shows an expired-session error when deactivate is answered with a login page', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url.endsWith('/deactivate')) return loginPageResponse();
        if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
        return envelope({ users: [activeUser], count: 1 });
      })
    );
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));

    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
  });

  it('surfaces a failed reactivate', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url.endsWith('/reactivate')) return jsonResponse({}, 500);
        if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
        return envelope({ users: [{ ...activeUser, status: 'locked' }], count: 1 });
      })
    );
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /reactivate/i }));

    await waitFor(() =>
      expect(screen.getByText('Failed to reactivate user (HTTP 500)')).toBeInTheDocument()
    );
  });

  it('renders users from the real { data: { users } } envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/users/invites')
          ? envelope({ invites: [], count: 0 })
          : envelope({ users: [activeUser], count: 1 })
      )
    );
    renderWithProviders(<Users />);
    expect(await screen.findByText('test-user')).toBeInTheDocument();
  });

  it('shows an error when the user list answers with a server error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/users/invites')
          ? envelope({ invites: [], count: 0 })
          : jsonResponse({ error: 'boom', status: 500 }, 500)
      )
    );
    renderWithProviders(<Users />);
    expect(await screen.findByText('Failed to load users')).toBeInTheDocument();
  });

  it('reloads the list after a successful deactivate', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.endsWith('/deactivate')) return envelope({ status: 'ok' });
      if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
      return envelope({ users: [activeUser], count: 1 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));

    await waitFor(() => {
      const listCalls = fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/users'));
      expect(listCalls).toHaveLength(2);
    });
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
