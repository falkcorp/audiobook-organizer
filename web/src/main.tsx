// file: web/src/main.tsx
// version: 2.0.0
// last-edited: 2026-08-20
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d

import React from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { CssBaseline, ThemeProvider } from '@mui/material';
import App from './App';
import { appTheme } from './theme';
import { ErrorBoundary } from './components/ErrorBoundary';
import { ToastProvider } from './components/toast/ToastProvider';
import { AuthProvider } from './contexts/AuthContext';
import { STORAGE_KEYS } from './lib/storageKeys';

// eslint-disable-next-line react-refresh/only-export-components
function AppRoot() {
  const app = (
    <ErrorBoundary>
      <BrowserRouter>
        {/* The theme is a module constant now, not derived from app state: with
            CSS variables, changing mode flips one attribute on <html> rather
            than rebuilding the theme and re-rendering every styled component.

            `modeStorageKey` is pointed at the key the zustand store already
            used, so an existing saved preference carries over without a
            migration step. The matching inline script in index.html applies it
            before React mounts, which is what removes the light-mode flash. */}
        <ThemeProvider
          theme={appTheme}
          defaultMode="dark"
          modeStorageKey={STORAGE_KEYS.APP_THEME_MODE}
        >
          <CssBaseline />
          <AuthProvider>
            <ToastProvider>
              <App />
            </ToastProvider>
          </AuthProvider>
        </ThemeProvider>
      </BrowserRouter>
    </ErrorBoundary>
  );

  return import.meta.env.DEV ? <React.StrictMode>{app}</React.StrictMode> : app;
}

ReactDOM.createRoot(document.getElementById('root')!).render(<AppRoot />);
