// file: web/src/App.health.test.tsx
// version: 1.1.1
// guid: 1755fd4f-dac4-437d-94d7-ad7d7ae6ed9c
// last-edited: 2026-11-07

// The post-shutdown reconnect poll runs before any session is trusted. When the
// Cloudflare Access session expired during the restart, /api/v1/health is
// answered with a login page. The poll must reload (to reach the sign-in page)
// rather than count another failed attempt behind the reconnect overlay forever.

import { act, render, screen } from '@testing-library/react';
import { BrowserRouter } from 'react-router';
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

vi.mock('./services/api', async (importOriginal) =>
  (await import('./test/appApiMock')).createAppApiMock(await importOriginal<typeof import('./services/api')>()),
);

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
