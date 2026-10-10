// file: web/src/pages/Setup.test.tsx
// version: 1.0.0
// guid: d163e7fe-4384-4367-8b6b-dd10bafb1576
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { loginPageResponse } from '../test/loginRedirect';
import Setup from './Setup';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => mockNavigate };
});

afterEach(() => {
  vi.unstubAllGlobals();
  mockNavigate.mockClear();
});

describe('Setup', () => {
  it('treats a login-page answer as "server needs login", not as a completed setup', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    const user = userEvent.setup();
    renderWithProviders(<Setup />);

    await user.type(screen.getByLabelText(/^username/i), 'test-admin');
    await user.type(screen.getByLabelText(/^password/i), 'correct-horse-1');
    await user.type(screen.getByLabelText(/confirm password/i), 'correct-horse-1');
    await user.click(screen.getByRole('button', { name: /create|setup|submit/i }));

    expect(await screen.findByText(/requires a login before setup/i)).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
