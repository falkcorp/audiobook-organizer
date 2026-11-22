// file: web/src/pages/Login.setup.test.tsx
// version: 0.1.0
// guid: 8d9a186e-b970-4941-b743-332b1b862b2e
// last-edited: 2026-11-22

// First-run setup from the sign-in page (F4-08 B10sq, G3): setup mode has the bootstrap-token
// field core needs from a machine other than the host, sends it, and names the two refusals
// (bootstrap_invalid, setup_closed) in their own words.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { coreJson, coreProblem, stubCoreFetch } from '../test/coreFetch';
import { setupAdmin as coreSetupAdmin } from '../services/coreIdentity';
import { BOOTSTRAP_INVALID_MESSAGE, SETUP_CLOSED_MESSAGE } from '../utils/setupErrorMessage';
import { Login } from './Login';

const login = vi.fn();
vi.mock('../contexts/AuthContext', () => ({
  useAuth: () => ({
    initialized: true,
    loading: false,
    user: null,
    requiresAuth: true,
    bootstrapReady: true,
    isAuthenticated: false,
    refresh: vi.fn(),
    login,
    logout: vi.fn(),
    // The context hands the payload to the real setup call.
    setupAdmin: (payload: Parameters<typeof coreSetupAdmin>[0]) => coreSetupAdmin(payload).then(() => undefined),
  }),
}));

afterEach(() => {
  vi.unstubAllGlobals();
  login.mockReset();
});

const adminBody = { id: 'u1', username: 'bruce.wayne', roles: ['admin'], status: 'active', created_at: 'x' };

async function fill(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/^username/i), 'bruce.wayne');
  await user.type(screen.getByLabelText(/^password/i), 'batcave-1939');
}

describe('Login setup mode', () => {
  it('offers the bootstrap-token field and sends what is typed', async () => {
    const { calls } = stubCoreFetch({
      'GET /auth/providers': coreJson({ providers: [] }),
      'POST /auth/setup': coreJson(adminBody, 201),
    });
    login.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderWithProviders(<Login />);
    await user.type(await screen.findByLabelText(/bootstrap token/i), 'btok-1');
    await fill(user);
    await user.click(screen.getByRole('button', { name: /create and login/i }));
    await waitFor(() => expect(login).toHaveBeenCalled());
    const post = calls.find((c) => c.method === 'POST' && c.path === '/auth/setup');
    expect(post?.body).toMatchObject({ username: 'bruce.wayne', password: 'batcave-1939', bootstrap_token: 'btok-1' });
  });

  it('sends no token when none is typed (a request from the host itself)', async () => {
    const { calls } = stubCoreFetch({
      'GET /auth/providers': coreJson({ providers: [] }),
      'POST /auth/setup': coreJson(adminBody, 201),
    });
    login.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderWithProviders(<Login />);
    await fill(user);
    await user.click(screen.getByRole('button', { name: /create and login/i }));
    await waitFor(() => expect(login).toHaveBeenCalled());
    expect(calls.find((c) => c.path === '/auth/setup')?.body).not.toHaveProperty('bootstrap_token');
  });

  it('says the token is wrong on a 401 bootstrap_invalid', async () => {
    stubCoreFetch({
      'GET /auth/providers': coreJson({ providers: [] }),
      'POST /auth/setup': coreProblem(401, 'bootstrap_invalid', 'the bootstrap token is wrong, expired or spent'),
    });
    const user = userEvent.setup();
    renderWithProviders(<Login />);
    await user.type(await screen.findByLabelText(/bootstrap token/i), 'btok-wrong');
    await fill(user);
    await user.click(screen.getByRole('button', { name: /create and login/i }));
    expect(await screen.findByText(BOOTSTRAP_INVALID_MESSAGE)).toBeInTheDocument();
    expect(login).not.toHaveBeenCalled();
  });

  it('says setup is not available on a 403 setup_closed', async () => {
    stubCoreFetch({
      'GET /auth/providers': coreJson({ providers: [] }),
      'POST /auth/setup': coreProblem(403, 'setup_closed', 'setup is not available'),
    });
    const user = userEvent.setup();
    renderWithProviders(<Login />);
    await fill(user);
    await user.click(screen.getByRole('button', { name: /create and login/i }));
    expect(await screen.findByText(SETUP_CLOSED_MESSAGE)).toBeInTheDocument();
    expect(login).not.toHaveBeenCalled();
  });
});
