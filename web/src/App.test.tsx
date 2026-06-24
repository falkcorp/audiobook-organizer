// file: web/src/App.test.tsx
// version: 1.0.9
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
// last-edited: 2026-06-24

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { BrowserRouter } from 'react-router-dom';
import { ThemeProvider } from '@mui/material';
import App from './App';
import { createAppTheme } from './theme';
import { AuthProvider } from './contexts/AuthContext';

const theme = createAppTheme('dark');

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

describe('App', () => {
  it('renders without crashing', async () => {
    render(
      <BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <ThemeProvider theme={theme}>
          <AuthProvider>
            <App />
          </AuthProvider>
        </ThemeProvider>
      </BrowserRouter>
    );

    expect(await screen.findByText('Audiobook Organizer')).toBeInTheDocument();
  });

  it('renders navigation items', async () => {
    render(
      <BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <ThemeProvider theme={theme}>
          <AuthProvider>
            <App />
          </AuthProvider>
        </ThemeProvider>
      </BrowserRouter>
    );

    expect((await screen.findAllByText('Dashboard')).length).toBeGreaterThan(0);
    expect((await screen.findAllByText('Library')).length).toBeGreaterThan(0);
  });
});
