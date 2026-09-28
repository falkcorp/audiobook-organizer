// file: web/src/hooks/useAdvancedSettings.ts
// version: 2.0.0
// guid: e7f8a9b0-c1d2-3456-efab-456789012345
// last-edited: 2026-09-27
//
// The global "Show advanced settings" switch.
//
// ONE VALUE, EVERY READER
//
// This used to be a plain useState seeded from localStorage, so each component
// that called the hook held its own copy. Flipping it on the Settings page left
// every other mounted reader (the review workspace's command menus, the Tools
// tab) on the old value until a reload. It is now a tiny external store read
// through useSyncExternalStore: a write notifies every subscriber in this tab,
// and the browser's `storage` event carries the change to other tabs.
//
// getSnapshot reads localStorage on every call rather than caching in a module
// variable. The snapshot is a boolean primitive, so React's identity check is
// satisfied, and a test that calls localStorage.clear() sees the default again
// instead of a value leaked from the previous test.
//
// Storage can be unavailable (private windows, blocked site data). Every access
// is wrapped: a read that throws means "off", a write that throws still updates
// this tab through an in-memory fallback so the switch does not look broken.

import { useCallback, useSyncExternalStore } from 'react';

export const ADVANCED_SETTINGS_STORAGE_KEY = 'settings.showAdvanced';

const listeners = new Set<() => void>();
/** Used only when localStorage itself throws; null means "not overridden". */
let memoryFallback: boolean | null = null;

function readShowAdvanced(): boolean {
  try {
    return localStorage.getItem(ADVANCED_SETTINGS_STORAGE_KEY) === 'true';
  } catch {
    return memoryFallback ?? false;
  }
}

function writeShowAdvanced(next: boolean): void {
  try {
    localStorage.setItem(ADVANCED_SETTINGS_STORAGE_KEY, String(next));
    memoryFallback = null;
  } catch {
    memoryFallback = next;
  }
  // The `storage` event only fires in OTHER tabs, so same-tab readers are told
  // here.
  listeners.forEach((l) => l());
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  const onStorage = (e: StorageEvent) => {
    // key === null is a localStorage.clear() in another tab.
    if (e.key === null || e.key === ADVANCED_SETTINGS_STORAGE_KEY) listener();
  };
  window.addEventListener('storage', onStorage);
  return () => {
    listeners.delete(listener);
    window.removeEventListener('storage', onStorage);
  };
}

export function useAdvancedSettings() {
  const showAdvanced = useSyncExternalStore(subscribe, readShowAdvanced, () => false);

  const setShowAdvanced = useCallback((next: boolean) => writeShowAdvanced(next), []);
  const toggleAdvanced = useCallback(() => writeShowAdvanced(!readShowAdvanced()), []);

  return { showAdvanced, setShowAdvanced, toggleAdvanced };
}
