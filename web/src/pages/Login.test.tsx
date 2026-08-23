// file: web/src/pages/Login.test.tsx
// version: 1.0.0
// guid: 8b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081
// last-edited: 2026-08-21

// Guards Login.tsx's redirectTo against an attacker-controlled
// location.state.from. Before this, an already-authenticated visit whose
// location.state.from was a malicious value (e.g. arriving via a crafted
// <Navigate state={{from: ...}}> or history.pushState from another script on
// the page) would be handed straight to navigate() with no validation — see
// safeReturn.test.ts for the sanitizeReturn allow-list this now runs through.

import { render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import { ThemeProvider } from '@mui/material';
import { appTheme } from '../theme';
import { Login } from './Login';
import { useAuth } from '../contexts/AuthContext';

vi.mock('../contexts/AuthContext', () => ({
  useAuth: vi.fn(),
}));

const mockUseAuth = vi.mocked(useAuth);

function authenticated() {
  return {
    initialized: true,
    loading: false,
    user: { id: 'u1', username: 'daisy', email: 'daisy@example.com' },
    requiresAuth: true,
    bootstrapReady: false,
    isAuthenticated: true,
    refresh: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    setupAdmin: vi.fn(),
  } as unknown as ReturnType<typeof useAuth>;
}

// Captures navigate() calls without performing real navigation, so the
// assertion is "what path was requested" rather than "what the router did
// with it" — MemoryRouter would otherwise 404 on an unregistered path and
// hide the very bug this test exists to catch.
const mockNavigate = vi.fn();
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

function renderLogin(from: string) {
  return render(
    <MemoryRouter initialEntries={[{ pathname: '/login', state: { from } }]}>
      <ThemeProvider theme={appTheme}>
        <Login />
      </ThemeProvider>
    </MemoryRouter>
  );
}

describe('Login redirectTo — rejects an unvalidated location.state.from', () => {
  it('falls back to /dashboard when state.from is protocol-relative (//evil.com)', async () => {
    mockNavigate.mockClear();
    mockUseAuth.mockReturnValue(authenticated());

    renderLogin('//evil.com');

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/dashboard', { replace: true });
    });
    expect(mockNavigate).not.toHaveBeenCalledWith(
      expect.stringContaining('evil.com'),
      expect.anything()
    );
  });

  it('honors a legitimate same-origin state.from', async () => {
    mockNavigate.mockClear();
    mockUseAuth.mockReturnValue(authenticated());

    renderLogin('/library?tag=metadata');

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/library?tag=metadata', { replace: true });
    });
  });
});
