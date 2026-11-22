// file: web/src/components/layout/UserMenu.errors.test.tsx
// version: 0.1.0
// guid: a422e151-52e7-4057-9fe5-eebe53de7c60
// last-edited: 2026-11-22

// The user menu shows what core said when a profile save or a password change is refused (F4-08
// B10sq, G2): a 422's field message, with no "ApiError:" prefix. The shell frame prints
// String(e); UserMenu hands it an error whose String() is the text alone.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { coreProblem, stubCoreFetch } from '../../test/coreFetch';

vi.mock('../../contexts/AuthContext', () => ({
  useAuth: () => ({
    requiresAuth: true,
    user: { id: 'u1', username: 'bruce.wayne', email: 'bruce@example.test' },
    logout: vi.fn(),
    refresh: vi.fn().mockResolvedValue(undefined),
  }),
}));

import { UserMenu } from './UserMenu';

afterEach(() => vi.unstubAllGlobals());

const validation = (field: string, message: string) =>
  new Response(
    JSON.stringify({
      title: 'Unprocessable Entity',
      status: 422,
      code: 'validation_failed',
      errors: [{ field, code: 'invalid', message }],
    }),
    { status: 422, headers: { 'Content-Type': 'application/problem+json' } }
  );

describe('UserMenu refusals', () => {
  it('shows a 422 on profile save as the field message alone', async () => {
    stubCoreFetch({ 'PATCH /me': validation('/body/email', 'use a plain ASCII address') });
    renderWithProviders(<UserMenu />);
    fireEvent.click(screen.getByRole('button', { name: /bruce.wayne/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Edit profile' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('use a plain ASCII address')).toBeInTheDocument();
    expect(screen.queryByText(/ApiError/)).toBeNull();
    expect(screen.queryByText(/Unprocessable Entity/)).toBeNull();
  });

  it('shows a 422 on password change as the field message alone', async () => {
    stubCoreFetch({ 'PUT /me/password': validation('/body/new', 'that password is on the list of common passwords') });
    renderWithProviders(<UserMenu />);
    fireEvent.click(screen.getByRole('button', { name: /bruce.wayne/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Change password' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Current password'), { target: { value: 'old-signal' } });
    fireEvent.change(within(dialog).getByLabelText('New password'), { target: { value: 'batcave-1939' } });
    fireEvent.change(within(dialog).getByLabelText('Confirm new password'), { target: { value: 'batcave-1939' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('that password is on the list of common passwords')).toBeInTheDocument();
    expect(screen.queryByText(/ApiError/)).toBeNull();
  });

  it('shows another refusal by its detail, also without the class name', async () => {
    stubCoreFetch({ 'PATCH /me': coreProblem(409, 'email_in_use', 'that e-mail address belongs to another account') });
    renderWithProviders(<UserMenu />);
    fireEvent.click(screen.getByRole('button', { name: /bruce.wayne/ }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Edit profile' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('that e-mail address belongs to another account')).toBeInTheDocument();
  });
});
