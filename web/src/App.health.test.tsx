// file: web/src/App.health.test.tsx
// version: 1.1.0
// guid: 1755fd4f-dac4-437d-94d7-ad7d7ae6ed9c
// last-edited: 2026-10-10

// The post-shutdown reconnect poll runs before any session is trusted. When the
// Cloudflare Access session expired during the restart, /api/v1/health is
// answered with a login page. The poll must reload (to reach the sign-in page)
// rather than count another failed attempt behind the reconnect overlay forever.

import { act, render, screen } from '@testing-library/react';
import { BrowserRouter } from 'react-router-dom';
import { ThemeProvider } from '@mui/material';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import { appTheme } from './theme';
import { AuthProvider } from './contexts/AuthContext';
import { loginPageResponse } from './test/loginRedirect';

const listeners = vi.hoisted(() => ({
  handlers: [] as Array<(event: { type: string }) => void>,
}));

vi.mock('./services/eventSourceManager', () => ({
  eventSourceManager: {
    subscribe: (handler: (event: { type: string }) => void) => {
      listeners.handlers.push(handler);
      return () => {
        listeners.handlers = listeners.handlers.filter((h) => h !== handler);
      };
    },
    subscribeStatus: () => () => {},
    close: vi.fn(),
    getStatus: () => ({ state: 'closed' }),
  },
}));

vi.mock('./services/api', () => ({
  getAuthStatus: vi.fn().mockResolvedValue({
    has_users: false,
    requires_auth: false,
    bootstrap_ready: false,
  }),
  getMe: vi.fn().mockResolvedValue(null),
  login: vi.fn(),
  logout: vi.fn(),
  setupAdmin: vi.fn(),
  getConfig: vi.fn().mockResolvedValue({ root_dir: '/tmp/library', setup_complete: true }),
  getHomeDirectory: vi.fn().mockResolvedValue('/home/user'),
  getSystemStatus: vi.fn().mockResolvedValue({
    status: 'ok',
    library: { book_count: 0, folder_count: 1, total_size: 0 },
    import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
    memory: {},
    runtime: {},
    operations: { recent: [] },
  }),
  getImportPaths: vi.fn().mockResolvedValue([{ path: '/tmp' }]),
  countBooks: vi.fn().mockResolvedValue(0),
  getOperationLogsTail: vi.fn().mockResolvedValue([]),
  getAuthors: vi.fn().mockResolvedValue([]),
  getSeries: vi.fn().mockResolvedValue([]),
  getNarrators: vi.fn().mockResolvedValue([]),
  getBooks: vi.fn().mockResolvedValue([]),
  searchBooks: vi.fn().mockResolvedValue([]),
  getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
  getAppVersion: vi.fn().mockResolvedValue('1.0.0-test'),
  getOperationTimeline: vi.fn().mockResolvedValue([]),
  openOperationsSSE: vi.fn().mockReturnValue({
    close: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
}));

beforeEach(() => {
  listeners.handlers = [];
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('App reconnect poll', () => {
  it('reloads the page when the health poll is answered with a login page', async () => {
    const reload = vi.fn();
    const realLocation = window.location;
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, reload },
    });
    try {
      const fetchMock = vi.fn(async (url: string) => {
        if (url === '/api/v1/health') return loginPageResponse();
        return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
      });
      vi.stubGlobal('fetch', fetchMock);

      render(
        <BrowserRouter>
          <ThemeProvider theme={appTheme}>
            <AuthProvider>
              <App />
            </AuthProvider>
          </ThemeProvider>
        </BrowserRouter>
      );
      await screen.findByText('Audiobook Organizer');

      vi.useFakeTimers();
      await act(async () => {
        listeners.handlers.forEach((h) => h({ type: 'system.shutdown' }));
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5100);
      });

      expect(fetchMock).toHaveBeenCalledWith('/api/v1/health', expect.anything());
      expect(reload).toHaveBeenCalledTimes(1);
      expect(screen.queryByText('Attempt 1')).not.toBeInTheDocument();
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
    }
  });

  it('keeps counting attempts, without reloading, while the server is unreachable', async () => {
    const reload = vi.fn();
    const realLocation = window.location;
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...realLocation, reload },
    });
    try {
      const fetchMock = vi.fn(async (url: string) => {
        if (url === '/api/v1/health') throw new TypeError('network unavailable');
        return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
      });
      vi.stubGlobal('fetch', fetchMock);

      render(
        <BrowserRouter>
          <ThemeProvider theme={appTheme}>
            <AuthProvider>
              <App />
            </AuthProvider>
          </ThemeProvider>
        </BrowserRouter>
      );
      await screen.findByText('Audiobook Organizer');

      vi.useFakeTimers();
      await act(async () => {
        listeners.handlers.forEach((h) => h({ type: 'system.shutdown' }));
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5100);
      });

      expect(screen.getByText('Attempt 1')).toBeInTheDocument();
      expect(reload).not.toHaveBeenCalled();
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: realLocation });
    }
  });
});
