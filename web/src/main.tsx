// file: web/src/main.tsx
// version: 1.4.1
// last-edited: 2026-08-06
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d

import React, { useMemo } from 'react';
import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { CssBaseline, ThemeProvider } from '@mui/material';
import App from './App';
import { createAppTheme } from './theme';
import { ErrorBoundary } from './components/ErrorBoundary';
import { ToastProvider } from './components/toast/ToastProvider';
import { useAppStore } from './stores/useAppStore';
import { AuthProvider } from './contexts/AuthContext';

// eslint-disable-next-line react-refresh/only-export-components
function AppRoot() {
  const themeMode = useAppStore((state) => state.themeMode);
  const theme = useMemo(() => createAppTheme(themeMode), [themeMode]);

  const app = (
    <ErrorBoundary>
      <BrowserRouter>
        <ThemeProvider theme={theme}>
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

  return import.meta.env.DEV ? (
    <React.StrictMode>{app}</React.StrictMode>
  ) : (
    app
  );
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <AppRoot />
);
