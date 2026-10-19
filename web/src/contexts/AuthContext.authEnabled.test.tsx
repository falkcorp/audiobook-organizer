// file: web/src/contexts/AuthContext.authEnabled.test.tsx
// version: 1.0.0
// guid: 7c1e5a42-93d8-4b6f-a0e1-5d2f8b3c9a74
// last-edited: 2026-10-18

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';

vi.mock('../services/api', async (orig) => ({
  ...(await orig<typeof import('../services/api')>()),
  getAuthStatus: vi.fn(),
  getMe: vi.fn(),
}));

import * as api from '../services/api';
import { AuthProvider, useAuth } from './AuthContext';

const wrapper = ({ children }: { children: ReactNode }) => <AuthProvider>{children}</AuthProvider>;

beforeEach(() => vi.resetAllMocks());

describe('AuthContext authEnabled', () => {
  it('is false when the server reports authentication off', async () => {
    vi.mocked(api.getAuthStatus).mockResolvedValue({
      has_users: false,
      auth_enabled: false,
      requires_auth: false,
      bootstrap_ready: false,
    });
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.initialized).toBe(true));
    expect(result.current.authEnabled).toBe(false);
  });

  it('is true with auth on but no users yet, while requiresAuth is false', async () => {
    vi.mocked(api.getAuthStatus).mockResolvedValue({
      has_users: false,
      auth_enabled: true,
      requires_auth: false,
      bootstrap_ready: true,
    });
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.initialized).toBe(true));
    expect(result.current.authEnabled).toBe(true);
    expect(result.current.requiresAuth).toBe(false);
  });

  it('stays unknown (null) when the status call never succeeds', async () => {
    vi.useFakeTimers();
    vi.mocked(api.getAuthStatus).mockRejectedValue(new Error('down'));
    const { result } = renderHook(() => useAuth(), { wrapper });
    await vi.advanceTimersByTimeAsync(5000);
    vi.useRealTimers();
    await waitFor(() => expect(result.current.initialized).toBe(true));
    expect(result.current.authEnabled).toBeNull();
  });
});
