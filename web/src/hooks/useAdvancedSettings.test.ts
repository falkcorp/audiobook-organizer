// file: web/src/hooks/useAdvancedSettings.test.ts
// version: 1.0.0
// guid: e7f8a9b0-c1d2-3456-efab-456789012344
// last-edited: 2026-06-15

import { renderHook, act } from '@testing-library/react';
import { useAdvancedSettings } from './useAdvancedSettings';

beforeEach(() => localStorage.clear());

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
