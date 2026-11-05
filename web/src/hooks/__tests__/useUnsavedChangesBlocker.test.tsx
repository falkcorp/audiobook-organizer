// file: web/src/hooks/__tests__/useUnsavedChangesBlocker.test.tsx
// version: 1.0.0
// guid: 6a1d7f30-92c4-4b85-8e3a-d05f1c7b2e94
// last-edited: 2026-11-05

import { act, renderHook } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import { useUnsavedChangesBlocker } from '../useUnsavedChangesBlocker';

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <MemoryRouter initialEntries={['/settings']}>{children}</MemoryRouter>
);

describe('useUnsavedChangesBlocker', () => {
  afterEach(() => {
    window.history.replaceState(null, '', '/');
  });

  it('lets a query-string or hash write on the same page through while dirty', () => {
    const { result } = renderHook(() => useUnsavedChangesBlocker(true), { wrapper });
    act(() => {
      window.history.replaceState(null, '', '/settings?tab=security');
    });
    expect(window.location.search).toBe('?tab=security');
    act(() => {
      window.history.replaceState(null, '', '#api-keys');
    });
    expect(result.current.state).toBe('idle');
  });

  it('still blocks leaving the page while dirty', () => {
    const { result } = renderHook(() => useUnsavedChangesBlocker(true), { wrapper });
    act(() => {
      window.history.pushState(null, '', '/library');
    });
    expect(result.current.state).toBe('blocked');
    expect(window.location.pathname).not.toBe('/library');
  });
});
