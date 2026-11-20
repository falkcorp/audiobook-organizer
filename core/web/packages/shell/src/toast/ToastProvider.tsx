// file: core/web/packages/shell/src/toast/ToastProvider.tsx
// version: 0.1.0
// guid: 7eeac4c0-948e-4671-878f-a70b5a79b786
// last-edited: 2026-11-20

// Toasts (07 §7.4.2 useToast): MUI Snackbar + Alert, queued, one at a time.
// An error toast has no auto-hide: it stays until dismissed.

import Alert from '@mui/material/Alert';
import Snackbar from '@mui/material/Snackbar';
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react';
import type { ToastApi, ToastSeverity } from '../contracts';

interface ToastItem {
  key: number;
  message: string;
  severity: ToastSeverity;
}

export const TOAST_AUTO_HIDE_MS = 5000;

const ToastContext = createContext<ToastApi | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [queue, setQueue] = useState<readonly ToastItem[]>([]);
  const [open, setOpen] = useState(true);
  const nextKey = useRef(0);

  const show = useCallback((message: string, severity: ToastSeverity = 'info') => {
    const key = nextKey.current++;
    setQueue((q) => [...q, { key, message, severity }]);
  }, []);
  const api = useMemo<ToastApi>(
    () => ({
      show,
      success: (message) => show(message, 'success'),
      error: (message) => show(message, 'error'),
    }),
    [show],
  );

  const current = queue[0];
  return (
    <ToastContext.Provider value={api}>
      {children}
      {current && (
        <Snackbar
          key={current.key}
          open={open}
          autoHideDuration={current.severity === 'error' ? null : TOAST_AUTO_HIDE_MS}
          onClose={(_event, reason) => {
            if (reason !== 'clickaway') setOpen(false);
          }}
          slotProps={{
            transition: {
              // The next toast is shown only once this one has left the screen.
              onExited: () => {
                setQueue((q) => q.slice(1));
                setOpen(true);
              },
            },
          }}
        >
          <Alert severity={current.severity} variant="filled" onClose={() => setOpen(false)}>
            {current.message}
          </Alert>
        </Snackbar>
      )}
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const api = useContext(ToastContext);
  if (!api) throw new Error('useToast must be used inside <ToastProvider>');
  return api;
}
