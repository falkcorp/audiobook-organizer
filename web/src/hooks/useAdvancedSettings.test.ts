// file: web/src/hooks/useAdvancedSettings.test.ts
// version: 2.0.0
// guid: e7f8a9b0-c1d2-3456-efab-456789012344
// last-edited: 2026-09-27

import { renderHook, act } from '@testing-library/react';
import { afterEach, vi } from 'vitest';
import { useAdvancedSettings } from './useAdvancedSettings';

beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

test('defaults to false', () => {
  const { result } = renderHook(() => useAdvancedSettings());
  expect(result.current.showAdvanced).toBe(false);
});

test('toggle flips value and persists to localStorage', () => {
  const { result } = renderHook(() => useAdvancedSettings());
  act(() => result.current.toggleAdvanced());
  expect(result.current.showAdvanced).toBe(true);
  expect(localStorage.getItem('settings.showAdvanced')).toBe('true');
});

test('reads persisted value on mount', () => {
  localStorage.setItem('settings.showAdvanced', 'true');
  const { result } = renderHook(() => useAdvancedSettings());
  expect(result.current.showAdvanced).toBe(true);
});

test('a change in one component reaches every other reader without a reload', () => {
  // The Settings switch and the review menus are different components; before
  // the shared store each held its own useState copy.
  const a = renderHook(() => useAdvancedSettings());
  const b = renderHook(() => useAdvancedSettings());
  act(() => a.result.current.setShowAdvanced(true));
  expect(b.result.current.showAdvanced).toBe(true);
  act(() => b.result.current.toggleAdvanced());
  expect(a.result.current.showAdvanced).toBe(false);
});

test('picks up a change made in another tab via the storage event', () => {
  const { result } = renderHook(() => useAdvancedSettings());
  act(() => {
    localStorage.setItem('settings.showAdvanced', 'true');
    window.dispatchEvent(new StorageEvent('storage', { key: 'settings.showAdvanced' }));
  });
  expect(result.current.showAdvanced).toBe(true);
});

test('blocked storage reads as off and does not throw', () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
    throw new Error('SecurityError');
  });
  const { result } = renderHook(() => useAdvancedSettings());
  expect(result.current.showAdvanced).toBe(false);
});
