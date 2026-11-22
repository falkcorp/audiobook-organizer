// file: core/web/packages/shell/test/appStore.test.ts
// version: 0.1.0
// guid: 9ec04d42-fa19-4af6-8bb0-554366b2b65d
// last-edited: 2026-11-22

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { useAppStore } from '../src/providers/appStore';

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
