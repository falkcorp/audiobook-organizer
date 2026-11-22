// file: web/src/pages/Users.signInRequired.test.tsx
// version: 0.1.0
// guid: e6dd1c1b-52f2-4ce5-8cc8-4f30d7012c22
// last-edited: 2026-11-22

// ABO's Users binding reads the books auth setting (authEnabled === false) and hands it to the
// shell page as authDisabled (SEC5-8).

import { afterEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { coreJson, stubCoreFetch } from '../test/coreFetch';

const authState = vi.hoisted(() => ({
  value: { initialized: true, authEnabled: false } as { initialized: boolean; authEnabled: boolean | null },
}));
vi.mock('../contexts/AuthContext', () => ({ useOptionalAuth: () => authState.value }));

import Users from './Users';

const bruce = { id: 'u1', username: 'bruce.wayne', roles: ['admin'], status: 'active', created_at: '2026-01-01T00:00:00Z' };
const serve = () =>
  stubCoreFetch({
    'GET /users': () => coreJson({ items: [bruce], total: 1 }),
    'GET /api/v1/users/invites': () => coreJson({ data: { invites: [], count: 0 } }),
    'GET /roles': () => coreJson({ items: [{ name: 'admin' }], total: 1 }),
  });

afterEach(() => vi.unstubAllGlobals());

describe('Users page binding', () => {
  it('passes auth-off through: notice shown, Create Invite disabled', async () => {
    authState.value = { initialized: true, authEnabled: false };
    serve();
    render(<MemoryRouter><Users /></MemoryRouter>);
    await screen.findByText('bruce.wayne');
    expect(screen.getByText(/Turn on sign-in to do this/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create Invite' })).toBeDisabled();
  });

  it('with auth on there is no notice', async () => {
    authState.value = { initialized: true, authEnabled: true };
    serve();
    render(<MemoryRouter><Users /></MemoryRouter>);
    await screen.findByText('bruce.wayne');
    expect(screen.queryByText(/Turn on sign-in to do this/)).toBeNull();
    expect(screen.getByRole('button', { name: 'Create Invite' })).toBeEnabled();
  });
});
