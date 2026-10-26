// file: web/src/hooks/useLibraryPermission.test.tsx
// version: 1.1.0
// guid: db29634b-a04f-459a-a061-6118f40e2d87
// last-edited: 2026-10-25

import { describe, it, expect } from 'vitest';
import { renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import {
  LIBRARY_DELETE_PERMISSION,
  LIBRARY_ORGANIZE_PERMISSION,
  holdsPermission,
  useHasPermission,
} from './useLibraryPermission';

const signedIn = (permissions?: string[]) => ({
  initialized: true,
  authEnabled: true as boolean | null,
  requiresAuth: true,
  user: { permissions },
});

describe('holdsPermission', () => {
  it('leaves the control alone when there is no auth information at all', () => {
    expect(holdsPermission(null, LIBRARY_DELETE_PERMISSION)).toBe(true);
  });

  it('is false until auth has initialised', () => {
    expect(
      holdsPermission(
        { ...signedIn([LIBRARY_DELETE_PERMISSION]), initialized: false },
        LIBRARY_DELETE_PERMISSION
      )
    ).toBe(false);
  });

  it('is open when authentication is explicitly off', () => {
    expect(
      holdsPermission(
        { initialized: true, authEnabled: false, requiresAuth: false, user: null },
        LIBRARY_DELETE_PERMISSION
      )
    ).toBe(true);
  });

  it('fails closed when the auth setting is unknown or nobody is signed in', () => {
    expect(
      holdsPermission(
        { initialized: true, authEnabled: null, requiresAuth: false, user: null },
        LIBRARY_DELETE_PERMISSION
      )
    ).toBe(false);
    expect(
      holdsPermission(
        { initialized: true, authEnabled: true, requiresAuth: true, user: null },
        LIBRARY_DELETE_PERMISSION
      )
    ).toBe(false);
  });

  it('is open on a first run: authentication on but no user created yet', () => {
    const firstRun = { initialized: true, authEnabled: true, requiresAuth: false, user: null };
    expect(holdsPermission(firstRun, LIBRARY_DELETE_PERMISSION)).toBe(true);
    expect(holdsPermission(firstRun, LIBRARY_ORGANIZE_PERMISSION)).toBe(true);
    // Not before auth has initialised, and not when the setting is unknown.
    expect(holdsPermission({ ...firstRun, initialized: false }, LIBRARY_DELETE_PERMISSION)).toBe(
      false
    );
    expect(holdsPermission({ ...firstRun, authEnabled: null }, LIBRARY_DELETE_PERMISSION)).toBe(
      false
    );
  });

  it('reads the signed-in user permission list, and a missing list means none', () => {
    const auth = signedIn(['library.edit_metadata', LIBRARY_DELETE_PERMISSION]);
    expect(holdsPermission(auth, LIBRARY_DELETE_PERMISSION)).toBe(true);
    expect(holdsPermission(auth, LIBRARY_ORGANIZE_PERMISSION)).toBe(false);
    expect(holdsPermission(signedIn(undefined), LIBRARY_DELETE_PERMISSION)).toBe(false);
  });
});

describe('useHasPermission', () => {
  it('does not throw outside an AuthProvider and leaves the control enabled', () => {
    const wrapper = ({ children }: { children: ReactNode }) => <>{children}</>;
    const { result } = renderHook(() => useHasPermission(LIBRARY_DELETE_PERMISSION), { wrapper });
    expect(result.current).toBe(true);
  });
});
