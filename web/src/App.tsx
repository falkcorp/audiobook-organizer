// file: web/src/App.tsx
// version: 1.23.0
// guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-08-10
import { useState, useEffect, useCallback, lazy, Suspense, useRef } from 'react';
import { STORAGE_KEYS } from './lib/storageKeys';
import { Routes, Route, Navigate } from 'react-router-dom';
import {
  Box,
  Backdrop,
  CircularProgress,
  Typography,
  Stack,
} from '@mui/material';
import { MainLayout } from './components/layout/MainLayout';
import { WelcomeWizard } from './components/wizard/WelcomeWizard';
import { KeyboardShortcutsDialog } from './components/KeyboardShortcutsDialog';
import { ErrorBoundary } from './components/ErrorBoundary';
import { Dashboard } from './pages/Dashboard';
import { Login } from './pages/Login';
import { eventSourceManager } from './services/eventSourceManager';
import * as api from './services/api';
import { useAuth } from './contexts/AuthContext';
import { useOperationsStore } from './stores/useOperationsStore';
import { useReviewStore } from './stores/useReviewStore';
import { useKeyboardShortcuts } from './hooks/useKeyboardShortcuts';

// Lazy-loaded pages (code-split for smaller initial bundle)
const Library = lazy(() =>
  import('./pages/Library').then((m) => ({ default: m.Library }))
);
const BookDetail = lazy(() =>
  import('./pages/BookDetail').then((m) => ({ default: m.BookDetail }))
);
const Works = lazy(() =>
  import('./pages/Works').then((m) => ({ default: m.Works }))
);
const System = lazy(() =>
  import('./pages/System').then((m) => ({ default: m.System }))
);
const Settings = lazy(() =>
  import('./pages/Settings').then((m) => ({ default: m.Settings }))
);
const FileBrowser = lazy(() =>
  import('./pages/FileBrowser').then((m) => ({ default: m.FileBrowser }))
);
const BookDedup = lazy(() =>
  import('./pages/BookDedup').then((m) => ({ default: m.BookDedup }))
);
const DedupLabels = lazy(() => import('./pages/DedupLabels'));
const Series = lazy(() =>
  import('./pages/Series').then((m) => ({ default: m.Series }))
);
const Authors = lazy(() =>
  import('./pages/Authors').then((m) => ({ default: m.Authors }))
);
const Diagnostics = lazy(() =>
  import('./pages/Diagnostics').then((m) => ({ default: m.Diagnostics }))
);
const ActivityLog = lazy(() => import('./pages/ActivityLog'));
const Playlists = lazy(() => import('./pages/Playlists'));
const PlaylistDetail = lazy(() => import('./pages/PlaylistDetail'));
const Setup = lazy(() => import('./pages/Setup'));
const Users = lazy(() => import('./pages/Users'));
const TrashedVersions = lazy(() => import('./pages/TrashedVersions'));
const ReviewQueue = lazy(() =>
  import('./pages/ReviewQueue').then((m) => ({ default: m.ReviewQueue }))
);

function App() {
  const auth = useAuth();
  const [showWizard, setShowWizard] = useState(false);
  const [wizardCheckComplete, setWizardCheckComplete] = useState(false);
  const [serverShutdown, setServerShutdown] = useState(false);
  const [reconnectAttempts, setReconnectAttempts] = useState(0);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);

  const handleShowShortcuts = useCallback(() => setShortcutsOpen(true), []);
  useKeyboardShortcuts({ onShowHelp: handleShowShortcuts });

  useEffect(() => {
    if (!auth.initialized) {
      return;
    }
    if (auth.requiresAuth && !auth.isAuthenticated) {
      setShowWizard(false);
      setWizardCheckComplete(true);
      return;
    }

    let cancelled = false;
    const checkWizardStatus = async () => {
      try {
        const config = await api.getConfig();
        const setupComplete =
          Boolean(config.root_dir && config.root_dir.trim()) ||
          Boolean(config.setup_complete);
        if (!cancelled) {
          setShowWizard(!setupComplete);
          if (setupComplete) {
            localStorage.setItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED, 'true');
          }
        }
      } catch (_error) {
        const wizardCompleted = localStorage.getItem(
          STORAGE_KEYS.WELCOME_WIZARD_COMPLETED
        );
        if (!cancelled) {
          setShowWizard(!wizardCompleted);
        }
      } finally {
        if (!cancelled) {
          setWizardCheckComplete(true);
        }
      }
    };

    checkWizardStatus();

    return () => {
      cancelled = true;
    };
  }, [auth.initialized, auth.isAuthenticated, auth.requiresAuth]);

  // Open the operations SSE connection and load recent ops once the user is
  // authenticated. This ensures the bell stays populated across page reloads.
  useEffect(() => {
    if (!auth.initialized || (auth.requiresAuth && !auth.isAuthenticated)) return;
    const store = useOperationsStore.getState();
    store.openSSE();
    void store.loadFromServer();
    return () => store.closeSSE();
  }, [auth.initialized, auth.isAuthenticated, auth.requiresAuth]);

  // Poll the review-queue count once the user is authenticated so the banner +
  // sidebar badge stay live. The interval lifecycle lives in the store (guarded
  // against double-start), mirroring the ops SSE effect above.
  useEffect(() => {
    if (!auth.initialized || (auth.requiresAuth && !auth.isAuthenticated)) return;
    const review = useReviewStore.getState();
    review.startPolling();
    return () => review.stopPolling();
  }, [auth.initialized, auth.isAuthenticated, auth.requiresAuth]);

  // Listen for server shutdown events and handle reconnection
  useEffect(() => {
    const unsubscribe = eventSourceManager.subscribe((event) => {
      if (event.type === 'system.shutdown') {
        setServerShutdown(true);
        eventSourceManager.close();
      }
    });

    return () => unsubscribe();
  }, []);

  // Reconnect attempts when server is down
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const isUnmountedRef = useRef(false);

  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, []);

  useEffect(() => {
    if (!serverShutdown) return;

    if (intervalRef.current) clearInterval(intervalRef.current);
    intervalRef.current = setInterval(async () => {
      if (isUnmountedRef.current) return;
      try {
        const response = await fetch('/api/v1/health');
        if (response.ok) {
          // Server is back, reload the page
          window.location.reload();
        }
      } catch (_e) {
        // Server still down, increment attempts
        if (!isUnmountedRef.current) {
          setReconnectAttempts((prev) => prev + 1);
        }
      }
    }, 5000); // Try every 5 seconds

    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current);
    };
  }, [serverShutdown]);

  const handleWizardComplete = () => {
    setShowWizard(false);
    // Force a full reload so dashboard fetches fresh data after setup
    window.location.reload();
  };

  if (!auth.initialized || !wizardCheckComplete) {
    return null;
  }

  const requiresLogin = auth.requiresAuth && !auth.isAuthenticated;

  return (
    <Box sx={{ display: 'flex', height: '100vh', overflow: 'hidden' }}>
      {/* Shutdown/Restart Overlay */}
      <Backdrop
        open={serverShutdown}
        sx={theme => ({
          color: '#fff',
          zIndex: theme.zIndex.drawer + 9999
        })}
      >
        <Stack spacing={3} alignItems="center">
          <CircularProgress color="inherit" size={60} />
          <Typography variant="h5">Server Shutting Down</Typography>
          <Typography variant="body1" sx={{ opacity: 0.8 }}>
            Attempting to reconnect...
          </Typography>
          {reconnectAttempts > 0 && (
            <Typography variant="caption" sx={{ opacity: 0.6 }}>
              Attempt {reconnectAttempts}
            </Typography>
          )}
        </Stack>
      </Backdrop>

      {!requiresLogin && (
        <WelcomeWizard open={showWizard} onComplete={handleWizardComplete} />
      )}

      {requiresLogin ? (
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="*" element={<Navigate to="/login" replace />} />
        </Routes>
      ) : (
        <MainLayout>
          <Suspense fallback={<CircularProgress sx={{ m: 4 }} />}>
            <Routes>
              <Route path="/" element={<Navigate to="/dashboard" replace />} />
              <Route path="/dashboard" element={<ErrorBoundary><Dashboard /></ErrorBoundary>} />
              <Route path="/library" element={<ErrorBoundary><Library defaultPreset="standard" /></ErrorBoundary>} />
              <Route path="/fingerprints" element={<ErrorBoundary><Library defaultPreset="fingerprints" /></ErrorBoundary>} />
              <Route path="/library/:id" element={<ErrorBoundary><BookDetail /></ErrorBoundary>} />
              <Route path="/works" element={<ErrorBoundary><Works /></ErrorBoundary>} />
              <Route path="/system" element={<ErrorBoundary><System /></ErrorBoundary>} />
              <Route path="/settings" element={<ErrorBoundary><Settings /></ErrorBoundary>} />
              <Route path="/login" element={<Navigate to="/dashboard" replace />} />
              <Route path="/files" element={<ErrorBoundary><FileBrowser /></ErrorBoundary>} />
              <Route path="/operations" element={<Navigate to="/activity" replace />} />
              <Route path="/maintenance" element={<Navigate to="/system" replace />} />
              <Route path="/authors/dedup" element={<Navigate to="/dedup" replace />} />
              <Route path="/books/dedup" element={<Navigate to="/dedup" replace />} />
              <Route path="/dedup" element={<ErrorBoundary><BookDedup /></ErrorBoundary>} />
              <Route path="/dedup/labels" element={<ErrorBoundary><DedupLabels /></ErrorBoundary>} />
              <Route path="/series" element={<ErrorBoundary><Series /></ErrorBoundary>} />
              <Route path="/authors" element={<ErrorBoundary><Authors /></ErrorBoundary>} />
              <Route path="/diagnostics" element={<ErrorBoundary><Diagnostics /></ErrorBoundary>} />
              <Route path="/activity" element={<ErrorBoundary><ActivityLog /></ErrorBoundary>} />
              <Route path="/playlists" element={<ErrorBoundary><Playlists /></ErrorBoundary>} />
              <Route path="/playlists/:id" element={<ErrorBoundary><PlaylistDetail /></ErrorBoundary>} />
              <Route path="/setup" element={<ErrorBoundary><Setup /></ErrorBoundary>} />
              <Route path="/users" element={<ErrorBoundary><Users /></ErrorBoundary>} />
              <Route path="/versions" element={<ErrorBoundary><TrashedVersions /></ErrorBoundary>} />
              <Route path="/review" element={<ErrorBoundary><ReviewQueue /></ErrorBoundary>} />
            </Routes>
          </Suspense>
        </MainLayout>
      )}
      <KeyboardShortcutsDialog
        open={shortcutsOpen}
        onClose={() => setShortcutsOpen(false)}
      />
    </Box>
  );
}

export default App;
