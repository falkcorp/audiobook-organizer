// file: web/src/App.notfound.test.tsx
// version: 1.1.0
// guid: 5d1e8a73-2c94-4b6f-8e07-a3f9c1d4b620
// last-edited: 2026-10-19

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { ThemeProvider } from '@mui/material';
import App from './App';
import { appTheme } from './theme';
import { AuthProvider } from './contexts/AuthContext';

const theme = appTheme;

// The real Settings page is rendered; only the API-keys panel body is stubbed so
// the test can tell whether the API Keys tab is the one that opened.
vi.mock('./components/settings/APIKeysTab', () => ({
  APIKeysTab: () => <div data-testid="api-keys-tab-body">API keys panel</div>,
}));

function LocationProbe() {
  const { pathname, hash } = useLocation();
  return <div data-testid="loc">{pathname + hash}</div>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ThemeProvider theme={theme}>
        <AuthProvider>
          <App />
          <LocationProbe />
        </AuthProvider>
      </ThemeProvider>
    </MemoryRouter>
  );
}

// Mock API
vi.mock('./services/api', async () => (await import('./test/appApiMock')).createAppApiMock());

beforeEach(() => {
  vi.clearAllMocks();
});

describe('signed-in catch-all and Audiobookshelf redirects', () => {
  it('shows the not-found page naming the requested path', async () => {
    renderAt('/no/such/page?x=1');
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument();
    expect(screen.getByTestId('not-found-path')).toHaveTextContent('/no/such/page?x=1');
    expect(screen.getByRole('link', { name: 'Go to Dashboard' })).toHaveAttribute(
      'href',
      '/dashboard'
    );
    expect(screen.getByRole('link', { name: 'Go to Settings' })).toHaveAttribute(
      'href',
      '/settings'
    );
  });

  it('shows not-found for an ABS path with no equivalent (/config/users)', async () => {
    renderAt('/config/users');
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument();
    expect(screen.getByTestId('loc')).toHaveTextContent('/config/users');
  });

  it.each([
    ['/config', '/settings'],
    ['/config/', '/settings'],
    ['/audiobookshelf/config', '/settings'],
    ['/audiobookshelf/config/', '/settings'],
    ['/audiobookshelf', '/dashboard'],
    ['/audiobookshelf/', '/dashboard'],
  ])('%s redirects to %s', async (path, target) => {
    renderAt(path);
    await waitFor(() => expect(screen.getByTestId('loc')).toHaveTextContent(target));
    expect(screen.getByTestId('loc').textContent).toBe(target);
    expect(screen.queryByText('Page not found')).not.toBeInTheDocument();
  });

  it.each([
    '/config/api-keys',
    '/config/api-keys/',
    '/audiobookshelf/config/api-keys',
    '/audiobookshelf/config/api-keys/',
  ])('%s redirects to /settings#api-keys and opens the API Keys tab', async (path) => {
    renderAt(path);
    expect(await screen.findByTestId('api-keys-tab-body')).toBeInTheDocument();
    expect(screen.getByTestId('loc')).toHaveTextContent('/settings#api-keys');
    expect(screen.getByRole('tab', { name: 'API Keys' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByText('Page not found')).not.toBeInTheDocument();
  });
});
