// file: web/src/stores/useAppStore.test.ts
// version: 1.0.0
// guid: f6a7b8c9-d0e1-4f2a-8b3c-4d5e6f7a8b9c
// last-edited: 2026-07-03

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { useAppStore } from './useAppStore';

// Regression coverage for an unbounded-memory-growth bug: error/warning
// notifications never auto-remove and the array had no length cap, so a
// long-running session with recurring errors grew the array without bound.

describe('useAppStore notifications', () => {
  beforeEach(() => {
    useAppStore.getState().clearNotifications();
    vi.useRealTimers();
  });

  it('caps the notifications array at 100, dropping the oldest', () => {
    const { addNotification } = useAppStore.getState();
    for (let i = 0; i < 105; i++) {
      addNotification(`error-${i}`, 'error');
    }
    const { notifications } = useAppStore.getState();
    expect(notifications).toHaveLength(100);
    expect(notifications[0].message).toBe('error-5');
    expect(notifications[notifications.length - 1].message).toBe('error-104');
  });

  it('still auto-removes success/info notifications after the timeout', () => {
    vi.useFakeTimers();
    const { addNotification } = useAppStore.getState();
    addNotification('saved', 'success');
    expect(useAppStore.getState().notifications).toHaveLength(1);
    vi.advanceTimersByTime(5000);
    expect(useAppStore.getState().notifications).toHaveLength(0);
    vi.useRealTimers();
  });
});
