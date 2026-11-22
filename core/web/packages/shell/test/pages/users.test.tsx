// file: core/web/packages/shell/test/pages/users.test.tsx
// version: 0.1.0
// guid: 5c1e8a30-9b47-4d62-a3f8-0e6d2b7c4a91
// last-edited: 2026-11-22

// F9-03 prep: UserPageExtensions carries rowActions that get {user, users,
// onDone(message?)} and panels that get {users, refreshKey, onNotice}; the
// shell's UsersPage bumps refreshKey and reloads when an action reports done.
// (Gaps 1 to 3 of seams.d/F9-04-s4-users.md.)

import { fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { defineModule } from '../../src/defineModule';
import type { UserPageUser, UserPanelProps, UserRowActionProps } from '../../src/types';
import { UsersPage } from '../../src/app/shellPages';
import { renderInShell } from '../../src/testing';

const manifest = {
  id: 'gotham',
  version: '0.1.0',
  displayName: 'Gotham',
  pages: [],
  nav: [],
  settingsPanels: [],
  widgets: [],
  banners: [],
  topBarItems: [],
  userPage: ['merge', 'keys', 'secret'],
  activityPanes: [],
  setupSteps: [],
  permissions: ['gotham:read', 'gotham:admin'],
} as const;

const users: UserPageUser[] = [
  { id: 'u1', username: 'bruce', status: 'active', roles: ['admin'] },
  { id: 'u2', username: 'dick', status: 'active', roles: ['user'], merged_into_user_id: 'u1' },
];

let lastRow: UserRowActionProps | undefined;
let lastPanel: UserPanelProps | undefined;

function MergeAction(props: UserRowActionProps) {
  if (props.user.id === 'u1') lastRow = props;
  return (
    <button type="button" onClick={() => props.onDone('Merge started')}>
      Merge {props.user.username}
    </button>
  );
}
function QuietAction(props: UserRowActionProps) {
  return (
    <button type="button" onClick={() => props.onDone()}>
      Quiet {props.user.username}
    </button>
  );
}
function KeysPanel(props: UserPanelProps) {
  lastPanel = props;
  return (
    <div>
      <span data-testid="refresh">{props.refreshKey}</span>
      <span data-testid="count">{props.users.length}</span>
      <button type="button" onClick={() => props.onNotice('Key reissued')}>
        reissue
      </button>
    </div>
  );
}

function gotham() {
  return defineModule(manifest, {
    routes: [],
    nav: [],
    settingsPanels: [],
    permissionsHints: { view: [] },
    userPageExtensions: {
      rowActions: [
        { id: 'merge', label: 'Merge', lazy: async () => ({ default: MergeAction }) },
        { id: 'secret', label: 'Quiet', lazy: async () => ({ default: QuietAction }), permission: 'gotham:admin' },
      ],
      panels: [{ id: 'keys', label: 'Phone keys', lazy: async () => ({ default: KeysPanel }) }],
    },
  });
}

describe('UsersPage slots', () => {
  it('gives each row action {user, users, onDone}', async () => {
    const reload = vi.fn();
    renderInShell(<UsersPage users={users} reload={reload} />, { modules: [gotham()], permissions: ['gotham:read'] });
    expect(await screen.findByRole('button', { name: 'Merge bruce' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Merge dick' })).toBeInTheDocument();
    expect(lastRow?.user).toEqual(users[0]);
    expect(lastRow?.users).toEqual(users);
    expect(typeof lastRow?.onDone).toBe('function');
  });

  it('hides a row action whose permission the user lacks, shows it to a holder', async () => {
    const { unmount } = renderInShell(<UsersPage users={users} reload={vi.fn()} />, { modules: [gotham()], permissions: ['gotham:read'] });
    await screen.findByRole('button', { name: 'Merge bruce' });
    expect(screen.queryByRole('button', { name: 'Quiet bruce' })).not.toBeInTheDocument();
    unmount();
    renderInShell(<UsersPage users={users} reload={vi.fn()} />, { modules: [gotham()], permissions: ['gotham:read', 'gotham:admin'] });
    expect(await screen.findByRole('button', { name: 'Quiet bruce' })).toBeInTheDocument();
  });

  it('onDone(message) shows the notice, reloads the list and bumps the panels refreshKey', async () => {
    const reload = vi.fn();
    renderInShell(<UsersPage users={users} reload={reload} />, { modules: [gotham()], permissions: ['gotham:read'] });
    expect((await screen.findByTestId('refresh')).textContent).toBe('0');
    fireEvent.click(screen.getByRole('button', { name: 'Merge bruce' }));
    expect(await screen.findByText('Merge started')).toBeInTheDocument();
    expect(reload).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('refresh').textContent).toBe('1');
  });

  it('onDone() with no message still reloads and bumps, and shows no notice', async () => {
    const reload = vi.fn();
    renderInShell(<UsersPage users={users} reload={reload} />, { modules: [gotham()], permissions: ['gotham:read', 'gotham:admin'] });
    fireEvent.click(await screen.findByRole('button', { name: 'Quiet bruce' }));
    expect(reload).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('refresh').textContent).toBe('1');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('a panel gets the users, and onNotice shows the message, reloads and bumps', async () => {
    const reload = vi.fn();
    renderInShell(<UsersPage users={users} reload={reload} />, { modules: [gotham()], permissions: ['gotham:read'] });
    expect((await screen.findByTestId('count')).textContent).toBe('2');
    expect(lastPanel?.refreshKey).toBe(0);
    fireEvent.click(screen.getByRole('button', { name: 'reissue' }));
    expect(await screen.findByText('Key reissued')).toBeInTheDocument();
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    expect(screen.getByTestId('refresh').textContent).toBe('1');
  });

  it('renders the panels title so the page says what each panel is', async () => {
    renderInShell(<UsersPage users={users} reload={vi.fn()} />, { modules: [gotham()], permissions: ['gotham:read'] });
    expect(await screen.findByRole('heading', { name: 'Phone keys' })).toBeInTheDocument();
  });
});
