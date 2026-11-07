// file: web/src/App.deeplinks.test.tsx
// version: 1.2.1
// guid: d2b84e61-3a07-4c5f-9e18-7f0a5c3b1d26
// last-edited: 2026-11-07

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { ThemeProvider } from '@mui/material';
import App from './App';
import { appTheme } from './theme';
import { AuthProvider } from './contexts/AuthContext';

// Tab bodies are stubbed: these tests are about which tab the URL selects and
// what a click writes back, not about what each tab renders.
vi.mock('./components/settings/APIKeysTab', () => ({
  APIKeysTab: () => <div data-testid="api-keys-tab-body">API keys panel</div>,
}));
vi.mock('./components/system/StorageTab', () => ({
  StorageTab: () => <div data-testid="storage-tab-body">storage</div>,
}));
vi.mock('./components/system/SystemInfoTab', () => ({
  SystemInfoTab: () => <div data-testid="info-tab-body">info</div>,
}));
vi.mock('./components/system/QuotaTab', () => ({
  QuotaTab: () => <div data-testid="quota-tab-body">quota</div>,
}));
vi.mock('./components/system/MaintenanceTab', () => ({
  MaintenanceTab: () => <div data-testid="maintenance-tab-body">maintenance</div>,
}));

vi.mock('./services/api', async () => (await import('./test/appApiMock')).createAppApiMock());

function LocationProbe() {
  const { pathname, search, hash } = useLocation();
  return <div data-testid="loc">{pathname + search + hash}</div>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ThemeProvider theme={appTheme}>
        <AuthProvider>
          <App />
          <LocationProbe />
        </AuthProvider>
      </ThemeProvider>
    </MemoryRouter>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('/system tabs', () => {
  it('opens on System Info with a bare URL', async () => {
    renderAt('/system');
    expect(await screen.findByTestId('info-tab-body')).toBeInTheDocument();
  });

  it('?tab=quota lands on Quota', async () => {
    renderAt('/system?tab=quota');
    expect(await screen.findByTestId('quota-tab-body')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Quota' })).toHaveAttribute('aria-selected', 'true');
  });

  it('a tab click writes ?tab= and the default tab leaves the URL bare', async () => {
    const user = userEvent.setup();
    renderAt('/system');
    await screen.findByTestId('info-tab-body');
    await user.click(screen.getByRole('tab', { name: 'Maintenance' }));
    expect(await screen.findByTestId('maintenance-tab-body')).toBeInTheDocument();
    expect(screen.getByTestId('loc')).toHaveTextContent('/system?tab=maintenance');
    await user.click(screen.getByRole('tab', { name: 'System Info' }));
    expect(screen.getByTestId('loc').textContent).toBe('/system');
  });
});

describe('/settings tabs', () => {
  it('?tab=api-keys lands on API Keys', async () => {
    renderAt('/settings?tab=api-keys');
    expect(await screen.findByTestId('api-keys-tab-body')).toBeInTheDocument();
  });

  it('still reads the older #api-keys form', async () => {
    renderAt('/settings#api-keys');
    expect(await screen.findByTestId('api-keys-tab-body')).toBeInTheDocument();
  });

  it('a tab click writes ?tab= (and drops the old hash); Temp Login has a slug', async () => {
    const user = userEvent.setup();
    renderAt('/settings#api-keys');
    await screen.findByTestId('api-keys-tab-body');
    await user.click(screen.getByRole('tab', { name: 'Temp Login' }));
    expect(screen.getByTestId('loc').textContent).toBe('/settings?tab=temp-login');
    await user.click(screen.getByRole('tab', { name: 'Library' }));
    expect(screen.getByTestId('loc').textContent).toBe('/settings');
  });
});

describe('legacy paths', () => {
  it('/operations keeps the query string, so ?op= still names the operation', async () => {
    renderAt('/operations?op=op-123');
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/activity?op=op-123'));
  });

  it('/operations with no query lands on a bare /activity', async () => {
    renderAt('/operations');
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/activity'));
  });

  it('/maintenance lands on the Maintenance tab', async () => {
    renderAt('/maintenance');
    expect(await screen.findByTestId('maintenance-tab-body')).toBeInTheDocument();
    expect(screen.getByTestId('loc').textContent).toBe('/system?tab=maintenance');
    expect(screen.getByRole('tab', { name: 'Maintenance' })).toHaveAttribute(
      'aria-selected',
      'true'
    );
  });
});
