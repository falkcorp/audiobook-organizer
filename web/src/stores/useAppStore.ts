// file: web/src/stores/useAppStore.ts
// version: 2.0.0
// guid: 1e2f3a4b-5c6d-7e8f-9a0b-1c2d3e4f5a6b
// last-edited: 2026-08-20

import { create } from 'zustand';
import { devtools } from 'zustand/middleware';

// error/warning notifications persist (no auto-remove timer), so without a
// cap the array grows unboundedly over a long session. Drop the oldest
// entry once the cap is reached.
const MAX_NOTIFICATIONS = 100;

// Colour mode used to live here. It now belongs to MUI's CSS-variable theme,
// which reads and writes STORAGE_KEYS.APP_THEME_MODE itself -- see main.tsx,
// which passes that key as `modeStorageKey`, and TopBar, which toggles through
// `useColorScheme()`. Keeping a second copy in this store would let the two
// disagree, which is exactly the bug CSS variables remove.

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
