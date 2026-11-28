// file: web/src/App.setupGate.test.tsx
// version: 0.1.0
// guid: 1007a7eb-2c02-4411-8b5a-132d56a92b51
// last-edited: 2026-11-21

// F9-04 slice 5: the first-run wizard gate. The wizard shows exactly when the library has no root
// directory and setup was never marked complete; when the config cannot be read, the stored
// "wizard completed" flag decides. Pinned before the gate moved into books' setup contribution.

import { render, screen, waitFor } from '@testing-library/react';
import { BrowserRouter } from 'react-router';
import { ThemeProvider } from '@mui/material';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import * as api from './services/api';
import { appTheme } from './theme';
import { AuthProvider } from './contexts/AuthContext';
import { STORAGE_KEYS } from './lib/storageKeys';

vi.mock('./services/eventSourceManager', () => ({
  eventSourceManager: {
    subscribe: () => () => {},
    subscribeStatus: () => () => {},
    close: vi.fn(),
    getStatus: () => ({ state: 'closed' }),
  },
}));

vi.mock('./components/tools/ToolsPanel', () => ({ ToolsPanel: () => null }));
vi.mock('./components/common/ServerFileBrowser', () => ({ ServerFileBrowser: () => null }));

vi.mock('./services/api', async (importOriginal) => ({
  ...(await import('./test/appApiMock')).createAppApiMock(await importOriginal<typeof import('./services/api')>()),
  // this file starts with no config answer, so each test supplies its own
  getConfig: vi.fn(),
}));

const renderApp = () =>
  render(
    <BrowserRouter>
      <ThemeProvider theme={appTheme}>
        <AuthProvider>
          <App />
        </AuthProvider>
      </ThemeProvider>
    </BrowserRouter>,
  );

const WIZARD = 'Welcome to Audiobook Organizer';

beforeEach(() => {
  vi.mocked(api.getConfig).mockReset();
  localStorage.clear();
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })));
});

afterEach(() => {
  vi.unstubAllGlobals();
  localStorage.clear();
});

const configCalls = () => vi.mocked(api.getConfig).mock.calls.length;

describe('first-run wizard gate (F9-04 slice 5)', () => {
  it('shows the wizard when there is no root directory and setup is not complete', async () => {
    vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', setup_complete: false } as never);
    renderApp();
    expect(await screen.findByText(WIZARD)).toBeInTheDocument();
    expect(localStorage.getItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED)).toBeNull();
  });

  it('treats a blank-only root directory as not set', async () => {
    vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '   ' } as never);
    renderApp();
    expect(await screen.findByText(WIZARD)).toBeInTheDocument();
  });

  it('does not show the wizard when a root directory is set, and remembers that', async () => {
    vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '/library/gotham', setup_complete: false } as never);
    renderApp();
    await waitFor(() => expect(localStorage.getItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED)).toBe('true'));
    expect(screen.queryByText(WIZARD)).toBeNull();
  });

  it('does not show the wizard when setup was marked complete without a root directory', async () => {
    vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', setup_complete: true } as never);
    renderApp();
    await waitFor(() => expect(localStorage.getItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED)).toBe('true'));
    expect(screen.queryByText(WIZARD)).toBeNull();
  });

  it('falls back to the stored flag when the config cannot be read: absent shows the wizard', async () => {
    vi.mocked(api.getConfig).mockRejectedValue(new Error('offline'));
    renderApp();
    expect(await screen.findByText(WIZARD)).toBeInTheDocument();
    expect(configCalls()).toBeGreaterThan(0);
  });

  it('falls back to the stored flag when the config cannot be read: present hides the wizard', async () => {
    localStorage.setItem(STORAGE_KEYS.WELCOME_WIZARD_COMPLETED, 'true');
    vi.mocked(api.getConfig).mockRejectedValue(new Error('offline'));
    renderApp();
    await waitFor(() => expect(configCalls()).toBeGreaterThan(0));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText(WIZARD)).toBeNull();
  });
});

// F4-08: the sign-in gate comes before the wizard. When the server requires sign-in and no
// session exists, the wizard is never shown and the config is never read (an anonymous PUT
// /api/v1/config is refused 401 under core identity: there is no first-run config window); the
// page offers Setup (no account yet) or Login instead, and Setup creates the first administrator
// through core's own route.
describe('the sign-in gate comes before the wizard (F4-08)', () => {
  it('requires sign-in with no account yet: Setup is shown, not the wizard, and the config is never read', async () => {
    vi.mocked(api.getAuthStatus).mockResolvedValue({ has_users: false, requires_auth: true, bootstrap_ready: true } as never);
    vi.mocked(api.getMe).mockResolvedValue(null as never);
    vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', setup_complete: false } as never);
    renderApp();
    expect(await screen.findByText('Create Admin Account')).toBeInTheDocument();
    expect(screen.queryByText(WIZARD)).toBeNull();
    expect(configCalls()).toBe(0);
  });

  it('requires sign-in with accounts: Login is shown, not the wizard, and the config is never read', async () => {
    vi.mocked(api.getAuthStatus).mockResolvedValue({ has_users: true, requires_auth: true, bootstrap_ready: false } as never);
    vi.mocked(api.getMe).mockResolvedValue(null as never);
    vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', setup_complete: false } as never);
    renderApp();
    expect(await screen.findByText('Sign in to access audiobook organizer.')).toBeInTheDocument();
    expect(screen.queryByText(WIZARD)).toBeNull();
    expect(configCalls()).toBe(0);
  });

  it("Setup creates the first administrator through core's POST /api/core/v1/auth/setup, with the bootstrap token", async () => {
    const actual = await vi.importActual<typeof import('./services/api')>('./services/api');
    const seen: { method: string; path: string; body: unknown }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://localhost'), init);
        seen.push({ method: req.method, path: new URL(req.url).pathname, body: JSON.parse(await req.clone().text()) });
        return new Response(JSON.stringify({ id: '01M72KQHXM3TJDMDBTC8G90416', username: 'bruce.wayne', roles: ['admin'] }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }),
    );
    await actual.setupAdmin({ username: 'bruce.wayne', password: 'correct-horse-batman-staple-9', bootstrap_token: ' gotham-token \n' });
    expect(seen).toEqual([
      {
        method: 'POST',
        path: '/api/core/v1/auth/setup',
        body: { username: 'bruce.wayne', password: 'correct-horse-batman-staple-9', bootstrap_token: 'gotham-token' },
      },
    ]);
  });
});
