// file: web/src/utils/fragmentToken.test.tsx
// version: 0.1.0
// guid: 4c4cba0f-f130-4bff-83fb-5170c541b8f6
// last-edited: 2026-11-22

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StrictMode } from 'react';
import { readFragmentToken, useFragmentToken } from './fragmentToken';

function Show() {
  const token = useFragmentToken();
  return <div data-testid="t">{token ?? 'none'}</div>;
}

beforeEach(() => window.history.replaceState({ router: 1 }, '', '/'));
afterEach(() => vi.restoreAllMocks());

describe('readFragmentToken', () => {
  it('reads token from the fragment', () => {
    window.history.replaceState(null, '', '/invite#token=abc');
    expect(readFragmentToken()).toBe('abc');
  });

  it('does not read the query string or the path (the old link forms)', () => {
    window.history.replaceState(null, '', '/invite?token=abc');
    expect(readFragmentToken()).toBeNull();
    window.history.replaceState(null, '', '/invite/abc');
    expect(readFragmentToken()).toBeNull();
  });
});

describe('useFragmentToken', () => {
  it('returns the token and removes the fragment with replaceState, keeping history state and the query', () => {
    window.history.replaceState({ router: 1 }, '', '/login/temp?next=1#token=abc');
    const replace = vi.spyOn(window.history, 'replaceState');
    const push = vi.spyOn(window.history, 'pushState');
    render(<Show />);
    expect(screen.getByTestId('t').textContent).toBe('abc');
    expect(window.location.hash).toBe('');
    expect(window.location.search).toBe('?next=1');
    expect(replace).toHaveBeenCalledWith({ router: 1 }, '', '/login/temp?next=1');
    expect(push).not.toHaveBeenCalled();
  });

  it('survives React double rendering: the token is still returned', () => {
    window.history.replaceState(null, '', '/invite#token=abc');
    render(
      <StrictMode>
        <Show />
      </StrictMode>
    );
    expect(screen.getByTestId('t').textContent).toBe('abc');
    expect(window.location.hash).toBe('');
  });

  it('leaves history alone when there is no token', () => {
    window.history.replaceState(null, '', '/invite');
    const replace = vi.spyOn(window.history, 'replaceState');
    render(<Show />);
    expect(screen.getByTestId('t').textContent).toBe('none');
    expect(replace).not.toHaveBeenCalled();
  });
});
