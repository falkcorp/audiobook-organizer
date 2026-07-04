// file: web/src/stores/useAppStore.ts
// version: 1.5.0
// guid: 1e2f3a4b-5c6d-7e8f-9a0b-1c2d3e4f5a6b
// last-edited: 2026-07-03

import { create } from 'zustand';
import { devtools } from 'zustand/middleware';
import { STORAGE_KEYS } from '../lib/storageKeys';

type ThemeMode = 'dark' | 'light';

// error/warning notifications persist (no auto-remove timer), so without a
// cap the array grows unboundedly over a long session. Drop the oldest
// entry once the cap is reached.
const MAX_NOTIFICATIONS = 100;

function readStoredThemeMode(): ThemeMode {
  if (typeof window === 'undefined') {
    return 'dark';
  }
  const stored = window.localStorage.getItem(STORAGE_KEYS.APP_THEME_MODE);
  return stored === 'light' || stored === 'dark' ? stored : 'dark';
}

function persistThemeMode(mode: ThemeMode): void {
  if (typeof window === 'undefined') {
    return;
  }
  window.localStorage.setItem(STORAGE_KEYS.APP_THEME_MODE, mode);
}

interface Notification {
  id: string;
  message: string;
  severity: 'success' | 'error' | 'warning' | 'info';
  timestamp: number;
  action?: { label: string; onClick: () => void };
}

interface AppState {
  // Loading states
  isLoading: boolean;
  setLoading: (loading: boolean) => void;

  // Theme preferences
  themeMode: ThemeMode;
  setThemeMode: (mode: ThemeMode) => void;
  toggleThemeMode: () => void;

  // Notifications
  notifications: Notification[];
  addNotification: (
    message: string,
    severity: Notification['severity'],
    action?: { label: string; onClick: () => void }
  ) => void;
  removeNotification: (id: string) => void;
  clearNotifications: () => void;

  // Error handling
  error: string | null;
  setError: (error: string | null) => void;
  clearError: () => void;
}

export const useAppStore = create<AppState>()(
  devtools(
    (set) => ({
      // Loading states
      isLoading: false,
      setLoading: (loading) => set({ isLoading: loading }),

      // Theme preferences
      themeMode: readStoredThemeMode(),
      setThemeMode: (mode) => {
        persistThemeMode(mode);
        set({ themeMode: mode });
      },
      toggleThemeMode: () =>
        set((state) => {
          const nextMode = state.themeMode === 'dark' ? 'light' : 'dark';
          persistThemeMode(nextMode);
          return { themeMode: nextMode };
        }),

      // Notifications
      notifications: [],
      addNotification: (message, severity, action) => {
        const id = `${Date.now()}-${Math.random()}`;
        set((state) => {
          const next = [
            ...state.notifications,
            { id, message, severity, timestamp: Date.now(), action },
          ];
          return {
            notifications:
              next.length > MAX_NOTIFICATIONS ? next.slice(next.length - MAX_NOTIFICATIONS) : next,
          };
        });
        // Auto-remove success/info after 5 seconds; error/warning persist
        if (severity === 'success' || severity === 'info') {
          const timeoutId = setTimeout(() => {
            set((state) => ({
              notifications: state.notifications.filter((n) => n.id !== id),
            }));
          }, 5000);
          // Cleanup on store unmount is handled by Zustand's built-in cleanup
          // Store this reference locally if needed for manual cancellation
          return () => clearTimeout(timeoutId);
        }
      },
      removeNotification: (id) =>
        set((state) => ({
          notifications: state.notifications.filter((n) => n.id !== id),
        })),
      clearNotifications: () => set({ notifications: [] }),

      // Error handling
      error: null,
      setError: (error) => set({ error }),
      clearError: () => set({ error: null }),
    }),
    { name: 'AppStore' }
  )
);
