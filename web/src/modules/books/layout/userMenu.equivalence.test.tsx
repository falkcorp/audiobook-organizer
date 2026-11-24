// file: web/src/modules/books/layout/userMenu.equivalence.test.tsx
// version: 0.1.0
// guid: 4f7a2c95-6d18-4e03-b9a1-0c3e8d5f7b62
// last-edited: 2026-11-21

// F9-02: the user menu behaves the same before and after it is cut into an
// audiobook-free frame over the shell's auth shape. Expected values are written
// out by hand from the component as it was before the cut; the legacy auth
// context and the two profile API calls are mocked at their modules, so the
// same mocks hold on both sides of the cut.

import { describe, it, expect, vi, beforeEach, type Mock } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../../test/renderWithProviders';
import { UserMenu } from '../../../components/layout/UserMenu';

const navigateSpy = vi.fn();
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigateSpy };
});

const auth = vi.hoisted(() => ({
  value: {} as Record<string, unknown>,
  logout: undefined as unknown as ReturnType<typeof vi.fn>,
  refresh: undefined as unknown as ReturnType<typeof vi.fn>,
}));
vi.mock('../../../contexts/AuthContext', () => ({ useAuth: () => auth.value }));

type AsyncFn = (...args: unknown[]) => Promise<unknown>;
const apiMock = vi.hoisted(() => ({
  updateMe: undefined as unknown as Mock<AsyncFn>,
  changePassword: undefined as unknown as Mock<AsyncFn>,
}));
vi.mock('../../../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../../services/api')>()),
  updateMe: (...a: unknown[]) => apiMock.updateMe(...a),
  changePassword: (...a: unknown[]) => apiMock.changePassword(...a),
}));

function signIn(over: Record<string, unknown> = {}) {
  auth.value = {
    requiresAuth: true,
    user: { id: 'u1', username: 'batman', email: 'bruce@wayne.example' },
    logout: auth.logout,
    refresh: auth.refresh,
    ...over,
  };
}

beforeEach(() => {
  navigateSpy.mockReset();
  auth.logout = vi.fn().mockResolvedValue(undefined);
  auth.refresh = vi.fn().mockResolvedValue(undefined);
  apiMock.updateMe = vi.fn<AsyncFn>().mockResolvedValue({});
  apiMock.changePassword = vi.fn<AsyncFn>().mockResolvedValue(undefined);
  signIn();
});

function open() {
  renderWithProviders(<UserMenu />);
  fireEvent.click(screen.getByRole('button', { name: /batman/ }));
}

describe('user menu equivalence: visibility', () => {
  it('renders nothing when the server does not require auth', () => {
    signIn({ requiresAuth: false });
    const { container } = renderWithProviders(<UserMenu />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders nothing without a user', () => {
    signIn({ user: null });
    const { container } = renderWithProviders(<UserMenu />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the username and its first two letters upper-cased', () => {
    renderWithProviders(<UserMenu />);
    expect(screen.getByText('batman')).toBeInTheDocument();
    expect(screen.getByText('BA')).toBeInTheDocument();
  });
});

describe('user menu equivalence: the menu', () => {
  it('lists the account, then Edit profile, Change password and Logout in order', () => {
    open();
    const menu = screen.getByRole('menu');
    expect(within(menu).getByText('bruce@wayne.example')).toBeInTheDocument();
    expect(within(menu).getAllByRole('menuitem').map((m) => m.textContent)).toEqual([
      'Edit profile',
      'Change password',
      'Logout',
    ]);
  });

  it('says "No email set" for an empty email', () => {
    signIn({ user: { id: 'u1', username: 'batman', email: '' } });
    open();
    expect(screen.getByText('No email set')).toBeInTheDocument();
  });

  it('logs out, then goes to /login', async () => {
    open();
    fireEvent.click(screen.getByRole('menuitem', { name: 'Logout' }));
    await waitFor(() => expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/login'));
    expect(auth.logout).toHaveBeenCalledTimes(1);
  });
});

describe('user menu equivalence: edit profile', () => {
  function openProfile() {
    open();
    fireEvent.click(screen.getByRole('menuitem', { name: 'Edit profile' }));
    return screen.getByRole('dialog');
  }

  it('opens with the username disabled and the email filled in', () => {
    const dialog = openProfile();
    expect(within(dialog).getByLabelText('Username')).toBeDisabled();
    expect(within(dialog).getByLabelText('Username')).toHaveValue('batman');
    expect(within(dialog).getByLabelText('Email')).toHaveValue('bruce@wayne.example');
  });

  it('refuses an empty email without calling the API', async () => {
    const dialog = openProfile();
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: '' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('Email is required')).toBeInTheDocument();
    expect(apiMock.updateMe).not.toHaveBeenCalled();
  });

  // The email field is type="email", whose value sanitisation strips
  // surrounding whitespace before the component sees it (jsdom and browsers
  // alike), so the component's own .trim() calls cannot be observed from here.
  it('saves the new email, refreshes the session and closes', async () => {
    const dialog = openProfile();
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'alfred@wayne.example' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(apiMock.updateMe).toHaveBeenCalledExactlyOnceWith({ email: 'alfred@wayne.example' });
    expect(auth.refresh).toHaveBeenCalledTimes(1);
  });

  it('saves on Enter in the email field', async () => {
    const dialog = openProfile();
    fireEvent.keyDown(within(dialog).getByLabelText('Email'), { key: 'Enter' });
    await waitFor(() => expect(apiMock.updateMe).toHaveBeenCalledTimes(1));
  });

  it('keeps the dialog open and shows the error when the save fails, without refreshing', async () => {
    apiMock.updateMe.mockRejectedValue(new Error('Email already taken'));
    const dialog = openProfile();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('Email already taken')).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(auth.refresh).not.toHaveBeenCalled();
  });

  it('disables Save and says "Saving…" while the request is in flight', async () => {
    let finish: () => void = () => undefined;
    apiMock.updateMe.mockReturnValue(new Promise<void>((r) => (finish = r)));
    const dialog = openProfile();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    const saving = await within(dialog).findByRole('button', { name: 'Saving…' });
    expect(saving).toBeDisabled();
    finish();
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('cancel closes without saving', async () => {
    const dialog = openProfile();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(apiMock.updateMe).not.toHaveBeenCalled();
  });
});

describe('user menu equivalence: change password', () => {
  function openPassword() {
    open();
    fireEvent.click(screen.getByRole('menuitem', { name: 'Change password' }));
    return screen.getByRole('dialog');
  }
  function fill(dialog: HTMLElement, cur: string, next: string, confirm: string) {
    fireEvent.change(within(dialog).getByLabelText('Current password'), { target: { value: cur } });
    fireEvent.change(within(dialog).getByLabelText('New password'), { target: { value: next } });
    fireEvent.change(within(dialog).getByLabelText('Confirm new password'), { target: { value: confirm } });
  }

  it('refuses a new password shorter than 8 characters', async () => {
    const dialog = openPassword();
    fill(dialog, 'old-pass', 'short', 'short');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('New password must be at least 8 characters')).toBeInTheDocument();
    expect(apiMock.changePassword).not.toHaveBeenCalled();
  });

  it('refuses a 7-character new password', async () => {
    const dialog = openPassword();
    fill(dialog, 'old-pass', '1234567', '1234567');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('New password must be at least 8 characters')).toBeInTheDocument();
    expect(apiMock.changePassword).not.toHaveBeenCalled();
  });

  it('accepts exactly 8 characters', async () => {
    const dialog = openPassword();
    fill(dialog, 'old-pass', '12345678', '12345678');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(apiMock.changePassword).toHaveBeenCalledTimes(1));
  });

  it('refuses when the confirmation differs', async () => {
    const dialog = openPassword();
    fill(dialog, 'old-pass', 'long-enough-1', 'long-enough-2');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('Passwords do not match')).toBeInTheDocument();
    expect(apiMock.changePassword).not.toHaveBeenCalled();
  });

  it('sends current and new password and closes on success', async () => {
    const dialog = openPassword();
    fill(dialog, 'old-pass', 'long-enough-1', 'long-enough-1');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(apiMock.changePassword).toHaveBeenCalledExactlyOnceWith({
      current_password: 'old-pass',
      new_password: 'long-enough-1',
    });
  });

  it('saves on Enter in the confirmation field', async () => {
    const dialog = openPassword();
    fill(dialog, 'old-pass', 'long-enough-1', 'long-enough-1');
    fireEvent.keyDown(within(dialog).getByLabelText('Confirm new password'), { key: 'Enter' });
    await waitFor(() => expect(apiMock.changePassword).toHaveBeenCalledTimes(1));
  });

  it('shows the error and stays open when the API refuses', async () => {
    apiMock.changePassword.mockRejectedValue(new Error('Current password is wrong'));
    const dialog = openPassword();
    fill(dialog, 'nope', 'long-enough-1', 'long-enough-1');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await within(dialog).findByText('Current password is wrong')).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('opens with empty fields and no error after an earlier failed attempt', async () => {
    let dialog = openPassword();
    fill(dialog, 'old-pass', 'short', 'short');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await within(dialog).findByText('New password must be at least 8 characters');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: /batman/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Change password' }));
    dialog = screen.getByRole('dialog');
    expect(within(dialog).getByLabelText('Current password')).toHaveValue('');
    expect(within(dialog).getByLabelText('New password')).toHaveValue('');
    expect(within(dialog).queryByText(/at least 8/)).not.toBeInTheDocument();
  });
});
