// file: web/src/hooks/useCanOrganizeLibrary.test.ts
// version: 1.0.0
// guid: 4b9d7e30-6c21-4f58-a3e1-9d05c8b2f716
// last-edited: 2026-10-23

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook } from '@testing-library/react';

vi.mock('../contexts/AuthContext', () => ({ useOptionalAuth: vi.fn() }));

import { useOptionalAuth } from '../contexts/AuthContext';
import { useCanOrganizeLibrary } from './useCanOrganizeLibrary';

const mockAuth = vi.mocked(useOptionalAuth);

function auth(over: { initialized?: boolean; authEnabled?: boolean | null; permissions?: string[] | null }) {
  mockAuth.mockReturnValue({
    initialized: over.initialized ?? true,
    authEnabled: over.authEnabled === undefined ? true : over.authEnabled,
    user: over.permissions === null ? null : ({ id: 'u1', permissions: over.permissions ?? [] } as never),
  } as never);
}

beforeEach(() => vi.resetAllMocks());

describe('useCanOrganizeLibrary', () => {
  it('is true only with library.organize when auth is on', () => {
    auth({ permissions: ['library.organize'] });
    expect(renderHook(() => useCanOrganizeLibrary()).result.current).toBe(true);
    auth({ permissions: ['library.view', 'library.edit_metadata'] });
    expect(renderHook(() => useCanOrganizeLibrary()).result.current).toBe(false);
  });

  it('fails closed outside a provider, before auth initialises, with the auth setting unknown, and signed out', () => {
    mockAuth.mockReturnValue(null);
    expect(renderHook(() => useCanOrganizeLibrary()).result.current).toBe(false);
    auth({ initialized: false, permissions: ['library.organize'] });
    expect(renderHook(() => useCanOrganizeLibrary()).result.current).toBe(false);
    auth({ authEnabled: null, permissions: [] });
    expect(renderHook(() => useCanOrganizeLibrary()).result.current).toBe(false);
    auth({ permissions: null });
    expect(renderHook(() => useCanOrganizeLibrary()).result.current).toBe(false);
  });

  it('is open when authentication is explicitly off', () => {
    auth({ authEnabled: false, permissions: null });
    expect(renderHook(() => useCanOrganizeLibrary()).result.current).toBe(true);
  });
});
