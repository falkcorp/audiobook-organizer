// file: web/src/components/users/usersSplit.test.tsx
// version: 0.1.0
// guid: 443a58bf-6a4f-4a9a-8858-cef4bb03c64c
// last-edited: 2026-11-21

// F9-04 slice 4: the Users page is split into the shell page (users, invites, footprint drawer,
// and slots) and books' user-page extensions (account merge row action and panel, phone app keys).
// The composed page must render and behave as it did before the cut.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import Users from '../../pages/Users';
import { UsersPage } from './UsersPage';
import type { UserPanelProps, UserRowActionProps } from './usersExtensions';
import { coreJson, stubCoreFetch } from '../../test/coreFetch';
import pageSource from './UsersPage.tsx?raw';

const envelope = (data: unknown) =>
  new Response(JSON.stringify({ data }), { status: 200, headers: { 'Content-Type': 'application/json' } });

const bruce = { id: 'u1', username: 'bruce.wayne', roles: ['admin'], status: 'active', created_at: '2026-01-01T00:00:00Z' };
const alfred = { id: 'u2', username: 'alfred.pennyworth', roles: ['admin'], status: 'active', created_at: '2026-01-02T00:00:00Z' };
const robin = { id: 'u3', username: 'dick.grayson', roles: ['editor'], status: 'active', created_at: '2026-01-03T00:00:00Z' };
const locked = { id: 'u4', username: 'harvey.dent', roles: ['editor'], status: 'disabled', created_at: '2026-01-04T00:00:00Z' };
const system = { id: 'u0', username: '_system', roles: ['admin'], status: 'active', created_at: '2026-01-01T00:00:00Z' };

const journal = {
  id: 'm1',
  status: 'applied',
  survivor_id: 'u2',
  absorbed_id: 'u3',
  ops: 4,
  cursor: 4,
  retired: false,
  conflict_total: 0,
  created_at: '2026-02-01T00:00:00Z',
  updated_at: '2026-02-01T00:00:00Z',
};

function serve() {
  const people = [system, bruce, alfred, robin, locked];
  return stubCoreFetch({
    'GET /users': () => coreJson({ items: people, total: people.length }),
    'GET /roles': () => coreJson({ items: [{ name: 'admin' }, { name: 'editor' }], total: 2 }),
    // Books' own routes: the pending-invite list and settings, and the extensions' reads.
    'GET /api/v1/users/invites': () => envelope({ invites: [], count: 0 }),
    'GET /api/v1/users/merges': () => envelope({ merges: [journal] }),
    'GET /api/v1/admin/guest-service-tokens': () => envelope({ flag_on: true, enabled: true, tokens: [] }),
  });
}

const follows = (a: HTMLElement, b: HTMLElement) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);

const renderUsers = () =>
  render(
    <MemoryRouter>
      <Users />
    </MemoryRouter>,
  );

afterEach(() => vi.unstubAllGlobals());

describe('Users composition (F9-04 slice 4)', () => {
  it('renders heading, rows, account merges and phone app keys in order', async () => {
    serve();
    renderUsers();
    const row = await screen.findByText('bruce.wayne');
    const ordered: HTMLElement[] = [
      screen.getByRole('heading', { name: 'Users' }),
      row,
      await screen.findByRole('heading', { name: 'Account merges' }),
      await screen.findByRole('heading', { name: 'Phone app keys' }),
    ];
    for (let i = 1; i < ordered.length; i++) {
      expect(follows(ordered[i - 1], ordered[i]), `element ${i} follows element ${i - 1}`).toBe(true);
    }
  });

  it('puts the merge button between the footprint and set-email buttons, only on active, unmerged, non-system rows', async () => {
    serve();
    renderUsers();
    await screen.findByText('bruce.wayne');
    const footprint = screen.getByRole('button', { name: 'Sign-ins and data for bruce.wayne' });
    const merge = screen.getByRole('button', { name: 'Merge bruce.wayne into another account' });
    const setEmail = screen.getByRole('button', { name: 'Set email for bruce.wayne' });
    expect(follows(footprint, merge)).toBe(true);
    expect(follows(merge, setEmail)).toBe(true);
    expect(screen.queryByRole('button', { name: /Merge _system/ })).toBeNull();
    expect(screen.queryByRole('button', { name: /Merge harvey.dent/ })).toBeNull();
    expect(screen.getAllByRole('button', { name: /^Merge .* into another account$/ })).toHaveLength(3);
  });

  it('the merge dialog offers only the other active admins as survivors, and closes again', async () => {
    serve();
    const user = userEvent.setup();
    renderUsers();
    await screen.findByText('bruce.wayne');
    await user.click(screen.getByRole('button', { name: 'Merge bruce.wayne into another account' }));
    expect(await screen.findByText('Merge bruce.wayne into…')).toBeInTheDocument();
    await user.click(screen.getByRole('combobox'));
    const options = screen.getAllByRole('option').map((o) => o.textContent);
    expect(options.join('|')).toContain('alfred.pennyworth');
    expect(options.join('|')).not.toContain('bruce.wayne');
    expect(options.join('|')).not.toContain('dick.grayson');
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: /cancel|close/i }));
    await waitFor(() => expect(screen.queryByText('Merge bruce.wayne into…')).toBeNull());
  });

  it('the shell page alone has no merge button and no panels', async () => {
    serve();
    render(
      <MemoryRouter>
        <UsersPage />
      </MemoryRouter>,
    );
    await screen.findByText('bruce.wayne');
    expect(screen.queryByRole('button', { name: /Merge .* into another account/ })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Account merges' })).toBeNull();
    expect(screen.queryByRole('heading', { name: 'Phone app keys' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Sign-ins and data for bruce.wayne' })).toBeInTheDocument();
  });

  it('gives a row action its row and the list, and a done call reloads, shows the notice and bumps panels', async () => {
    const { calls } = serve();
    const user = userEvent.setup();
    const seenRefresh: number[] = [];
    const Action = ({ user: u, users, onDone }: UserRowActionProps) =>
      u.id === 'u1' ? (
        <button onClick={() => onDone('Batarang merge started')}>{`act ${u.username} of ${users.length}`}</button>
      ) : null;
    const Panel = ({ refreshKey, onNotice }: UserPanelProps) => {
      seenRefresh.push(refreshKey);
      return <button onClick={() => onNotice('panel said hi')}>panel</button>;
    };
    render(
      <MemoryRouter>
        <UsersPage extensions={{ rowActions: [Action], panels: [Panel] }} />
      </MemoryRouter>,
    );
    await user.click(await screen.findByRole('button', { name: 'act bruce.wayne of 5' }));
    expect(await screen.findByText('Batarang merge started')).toBeInTheDocument();
    const usersLoads = () => calls.filter((c) => c.method === 'GET' && c.path.startsWith('/users?')).length;
    await waitFor(() => expect(usersLoads()).toBe(2));
    expect(Math.max(...seenRefresh)).toBe(1);
    await user.click(screen.getByRole('button', { name: 'panel' }));
    expect(await screen.findByText('panel said hi')).toBeInTheDocument();
    await waitFor(() => expect(usersLoads()).toBe(3));
    // a panel notice reloads the list but is not a row-action change
    expect(Math.max(...seenRefresh)).toBe(1);
  });

  it('the shell page imports no books code', () => {
    const imports = pageSource.split('\n').filter((l) => /from '/.test(l));
    expect(imports.length).toBeGreaterThan(3);
    for (const l of imports) expect(l).not.toMatch(/modules\/books|UserMerge'|GuestPhoneKeys|services\/api/);
  });
});
