// file: web/src/pages/Setup.test.tsx
// version: 1.0.1
// guid: d163e7fe-4384-4367-8b6b-dd10bafb1576
// last-edited: 2026-11-07

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { coreJson, coreProblem, stubCoreFetch } from '../test/coreFetch';
import Setup from './Setup';

const mockNavigate = vi.fn();
vi.mock('react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router')>();
  return { ...actual, useNavigate: () => mockNavigate };
});

afterEach(() => {
  vi.unstubAllGlobals();
  mockNavigate.mockClear();
});

async function fillAndSubmit(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText(/^username/i), 'test-admin');
  await user.type(screen.getByLabelText(/^password/i), 'correct-horse-1');
  await user.type(screen.getByLabelText(/confirm password/i), 'correct-horse-1');
  await user.click(screen.getByRole('button', { name: /create|setup|submit/i }));
}

describe('Setup', () => {
  it('sends the bootstrap token when one is typed and goes home on success', async () => {
    const { calls } = stubCoreFetch({
      'POST /auth/setup': coreJson(
        { id: 'u1', username: 'test-admin', roles: ['admin'], status: 'active', created_at: 'x' },
        201
      ),
    });
    const user = userEvent.setup();
    renderWithProviders(<Setup />);
    await user.type(screen.getByLabelText(/bootstrap token/i), 'btok-1');
    await fillAndSubmit(user);
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/'));
    expect(calls[0]).toMatchObject({
      method: 'POST',
      path: '/auth/setup',
      body: { username: 'admintest-admin', password: 'correct-horse-1', bootstrap_token: 'btok-1' },
    });
  });

  it('shows the refusal the server gave and does not navigate', async () => {
    stubCoreFetch({
      'POST /auth/setup': coreProblem(403, 'setup_closed', 'the bootstrap token is required'),
    });
    const user = userEvent.setup();
    renderWithProviders(<Setup />);
    await fillAndSubmit(user);
    expect(await screen.findByText(/bootstrap token is required/i)).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
