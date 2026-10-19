// file: web/src/App.notfound.test.tsx
// version: 1.0.0
// guid: 5d1e8a73-2c94-4b6f-8e07-a3f9c1d4b620
// last-edited: 2026-10-19

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
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
  getConfig: vi.fn().mockResolvedValue({
    root_dir: '/tmp/library',
    setup_complete: true,
  }),
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
  getSystemStorage: vi.fn().mockResolvedValue({
    path: '/',
    total_bytes: 1000000,
    used_bytes: 500000,
    free_bytes: 500000,
    percent_used: 50,
    quota_enabled: false,
    quota_percent: 0,
    user_quotas_enabled: false,
  }),
  openOperationsSSE: vi.fn().mockReturnValue({
    close: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
}));

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

  it.each(['/config/api-keys', '/config/api-keys/'])(
    '%s redirects to /settings#api-keys and opens the API Keys tab',
    async (path) => {
      renderAt(path);
      expect(await screen.findByTestId('api-keys-tab-body')).toBeInTheDocument();
      expect(screen.getByTestId('loc')).toHaveTextContent('/settings#api-keys');
      expect(screen.getByRole('tab', { name: 'API Keys' })).toHaveAttribute(
        'aria-selected',
        'true'
      );
      expect(screen.queryByText('Page not found')).not.toBeInTheDocument();
    }
  );
});
