// file: web/src/modules/books/pages/UserMergeRowAction.test.tsx
// version: 0.1.0
// guid: cbfb181e-2f93-4581-b8bb-63b09d8eb58a
// last-edited: 2026-11-21

// F9-04 slice 4: books' merge row action and extension list, against a stub merge dialog.

import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { isSystemAccount, type MergeableUser } from '../../../components/users/userAccountParts';
import { UserMergeRowAction } from './UserMergeRowAction';
import { booksUsersExtensions } from './usersExtensions';
import { UserMergesPanel } from './UserMerge';
import { GuestPhoneKeysPanel } from './GuestPhoneKeysPanel';

vi.mock('./UserMerge', async () => ({
  ...(await vi.importActual<typeof import('./UserMerge')>('./UserMerge')),
  UserMergeDialog: ({
    absorbed,
    candidates,
    onClose,
    onStarted,
  }: {
    absorbed: MergeableUser | null;
    candidates: MergeableUser[];
    onClose: () => void;
    onStarted: (op: string, a: MergeableUser) => void;
  }) =>
    absorbed ? (
      <div role="dialog">
        <p data-testid="candidates">{candidates.map((c) => c.username).join(',')}</p>
        <button onClick={() => onStarted('op-7', absorbed)}>start</button>
        <button onClick={onClose}>close</button>
      </div>
    ) : null,
}));

const bruce: MergeableUser = { id: 'u1', username: 'bruce.wayne', status: 'active', roles: ['admin'] };
const alfred: MergeableUser = { id: 'u2', username: 'alfred.pennyworth', status: 'active', roles: ['admin'] };
const dick: MergeableUser = { id: 'u3', username: 'dick.grayson', status: 'active', roles: ['editor'] };
const harvey: MergeableUser = { id: 'u4', username: 'harvey.dent', status: 'active', roles: ['admin'], merged_into_user_id: 'u2' };
const everyone = [bruce, alfred, dick, harvey];

describe('UserMergeRowAction', () => {
  it('is hidden for the system account, locked accounts and accounts already merged away', () => {
    for (const u of [
      { ...bruce, username: '_system' },
      { ...bruce, status: 'locked' },
      harvey,
    ]) {
      const { unmount } = render(<UserMergeRowAction user={u} users={everyone} onDone={vi.fn()} />);
      expect(screen.queryByRole('button')).toBeNull();
      unmount();
    }
  });

  it('opens with only the other active, unmerged admins as survivors and closes again', async () => {
    const user = userEvent.setup();
    render(<UserMergeRowAction user={bruce} users={everyone} onDone={vi.fn()} />);
    expect(screen.queryByRole('dialog')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Merge bruce.wayne into another account' }));
    expect(screen.getByTestId('candidates')).toHaveTextContent(/^alfred\.pennyworth$/);
    await user.click(screen.getByRole('button', { name: 'close' }));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('never offers a system account as a survivor, whatever status or roles it carries', async () => {
    const user = userEvent.setup();
    const systemByName: MergeableUser = { id: 'u8', username: '_system', status: 'active', roles: ['admin'] };
    const systemByStatus: MergeableUser = { id: 'u9', username: 'batcomputer', status: 'system', roles: ['admin'] };
    render(
      <UserMergeRowAction user={bruce} users={[...everyone, systemByName, systemByStatus]} onDone={vi.fn()} />,
    );
    await user.click(screen.getByRole('button', { name: 'Merge bruce.wayne into another account' }));
    expect(screen.getByTestId('candidates')).toHaveTextContent(/^alfred\.pennyworth$/);
  });

  it('is hidden for an account with the system status, not only the _system name', () => {
    render(
      <UserMergeRowAction
        user={{ ...bruce, username: 'batcomputer', status: 'system' }}
        users={everyone}
        onDone={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('recognises a system account by its name or by its status', () => {
    expect(isSystemAccount({ username: '_system', status: 'active' })).toBe(true);
    expect(isSystemAccount({ username: 'batcomputer', status: 'system' })).toBe(true);
    expect(isSystemAccount({ username: 'bruce.wayne', status: 'active' })).toBe(false);
  });

  it('reports a started merge through onDone with the notice, and closes', async () => {
    const user = userEvent.setup();
    const onDone = vi.fn();
    render(<UserMergeRowAction user={bruce} users={everyone} onDone={onDone} />);
    await user.click(screen.getByRole('button', { name: 'Merge bruce.wayne into another account' }));
    await user.click(screen.getByRole('button', { name: 'start' }));
    expect(onDone).toHaveBeenCalledWith(
      'Merging bruce.wayne started (operation op-7). It is listed under Account merges when it has begun.',
    );
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('booksUsersExtensions lists the merge action, then the merges panel and the phone app keys', () => {
    expect(booksUsersExtensions.rowActions).toEqual([UserMergeRowAction]);
    expect(booksUsersExtensions.panels).toEqual([UserMergesPanel, GuestPhoneKeysPanel]);
  });
});
