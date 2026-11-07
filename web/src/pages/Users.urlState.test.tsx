// file: web/src/pages/Users.urlState.test.tsx
// version: 1.0.1
// guid: 5c8e2d17-90b3-4a46-8f2e-1d7b63a04c95
// last-edited: 2026-11-07

/**
 * Users deep link: `?footprint=<userId>` opens that user's sign-ins-and-data
 * drawer, and opening or closing the drawer writes it (replace).
 */

import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation, useNavigationType } from 'react-router';
import Users from './Users';

const envelope = (data: unknown) =>
  new Response(JSON.stringify({ data }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });

const batman = {
  id: 'u1',
  username: 'bruce.wayne',
  role_id: 'editor',
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
};

function serve() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
      if (url.includes('/footprint'))
        return envelope({
          user: { id: 'u1', username: 'bruce.wayne', status: 'active', has_password: true },
          identities: [],
          counts: {},
          active_sessions: 0,
          unreadable: [],
        });
      return envelope({ users: [batman], count: 1 });
    })
  );
}

function Probe() {
  const l = useLocation();
  const t = useNavigationType();
  return (
    <>
      <div data-testid="loc">{`${l.pathname}${l.search}`}</div>
      <div data-testid="nav">{t}</div>
    </>
  );
}

const renderAt = (entry: string) =>
  render(
    <MemoryRouter initialEntries={[entry]}>
      <Users />
      <Probe />
    </MemoryRouter>
  );

afterEach(() => vi.unstubAllGlobals());

describe('Users ?footprint=', () => {
  it("opens that user's drawer once the list has loaded, and keeps the link", async () => {
    serve();
    renderAt('/users?footprint=u1');
    expect(await screen.findByRole('region', { name: 'Sign-ins & data' })).toBeInTheDocument();
    expect(screen.getByText(/Sign-ins & data: bruce.wayne/)).toBeInTheDocument();
    expect(screen.getByTestId('loc').textContent).toBe('/users?footprint=u1');
  });

  it('an id nobody has opens nothing', async () => {
    serve();
    renderAt('/users?footprint=nobody');
    await screen.findByText('bruce.wayne');
    expect(screen.queryByRole('region', { name: 'Sign-ins & data' })).not.toBeInTheDocument();
  });

  it('the row button writes ?footprint= with replace; closing the drawer removes it', async () => {
    serve();
    const user = userEvent.setup();
    renderAt('/users');
    await user.click(
      await screen.findByRole('button', { name: 'Sign-ins and data for bruce.wayne' })
    );
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/users?footprint=u1'));
    expect(screen.getByTestId('nav').textContent).toBe('REPLACE');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/users'));
  });
});
