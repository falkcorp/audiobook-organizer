// file: web/src/components/layout/UserMenuFrame.test.tsx
// version: 0.1.0
// guid: 3c8f1e52-7a96-4d04-b2e8-a5d0c9f7b146
// last-edited: 2026-11-21

// UserMenuFrame reads the shell's auth shape and takes its two server calls as
// props, so this test drives it with a Gotham AuthState of its own and no
// audiobook module. It moves into core/web/packages/shell/src/layout/ with the
// frame.

import { describe, it, expect, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { UserMenuFrame, type UserMenuFrameProps } from './UserMenuFrame';

const navigateSpy = vi.fn();
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigateSpy };
});

function props(over: Partial<UserMenuFrameProps['auth']> = {}): UserMenuFrameProps {
  return {
    auth: {
      status: 'authenticated',
      user: { id: 'u1', username: 'robin', email: 'dick@wayne.example' },
      logout: vi.fn().mockResolvedValue(undefined),
      refresh: vi.fn().mockResolvedValue(undefined),
      ...over,
    },
    onSaveProfile: vi.fn().mockResolvedValue(undefined),
    onChangePassword: vi.fn().mockResolvedValue(undefined),
  };
}

describe('UserMenuFrame', () => {
  it.each(['loading', 'anonymous', 'error'] as const)('renders nothing while status is %s', (status) => {
    const { container } = renderWithProviders(<UserMenuFrame {...props({ status })} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders nothing when authenticated without a user (auth disabled)', () => {
    const { container } = renderWithProviders(<UserMenuFrame {...props({ user: null })} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the user, and a missing email as "No email set"', () => {
    renderWithProviders(<UserMenuFrame {...props({ user: { id: 'u1', username: 'robin' } })} />);
    expect(screen.getByText('RO')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /robin/ }));
    expect(screen.getByText('No email set')).toBeInTheDocument();
  });

  it('hands the typed email to onSaveProfile, then refreshes auth, in that order', async () => {
    const p = props();
    const order: string[] = [];
    (p.onSaveProfile as ReturnType<typeof vi.fn>).mockImplementation(async () => void order.push('save'));
    (p.auth.refresh as ReturnType<typeof vi.fn>).mockImplementation(async () => void order.push('refresh'));
    renderWithProviders(<UserMenuFrame {...p} />);
    fireEvent.click(screen.getByRole('button', { name: /robin/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Edit profile' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'tim@wayne.example' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(p.onSaveProfile).toHaveBeenCalledExactlyOnceWith('tim@wayne.example');
    expect(order).toEqual(['save', 'refresh']);
  });

  it('hands both passwords to onChangePassword as positional arguments', async () => {
    const p = props();
    renderWithProviders(<UserMenuFrame {...p} />);
    fireEvent.click(screen.getByRole('button', { name: /robin/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Change password' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Current password'), { target: { value: 'old-signal' } });
    fireEvent.change(within(dialog).getByLabelText('New password'), { target: { value: 'new-signal-1' } });
    fireEvent.change(within(dialog).getByLabelText('Confirm new password'), { target: { value: 'new-signal-1' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(p.onChangePassword).toHaveBeenCalledTimes(1));
    expect(p.onChangePassword).toHaveBeenCalledWith('old-signal', 'new-signal-1');
  });

  it('shows a rejected save and does not refresh', async () => {
    const p = props();
    (p.onSaveProfile as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Bat-mail bounced'));
    renderWithProviders(<UserMenuFrame {...p} />);
    fireEvent.click(screen.getByRole('button', { name: /robin/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Edit profile' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Error: Bat-mail bounced')).toBeInTheDocument();
    expect(p.auth.refresh).not.toHaveBeenCalled();
  });

  it('logs out through auth.logout and goes to /login', async () => {
    navigateSpy.mockReset();
    const p = props();
    renderWithProviders(<UserMenuFrame {...p} />);
    fireEvent.click(screen.getByRole('button', { name: /robin/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Logout' }));
    await waitFor(() => expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/login'));
    expect(p.auth.logout).toHaveBeenCalledTimes(1);
  });
});
