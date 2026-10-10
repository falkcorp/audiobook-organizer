// file: web/src/components/settings/DelugeSettingsTab.test.tsx
// version: 1.0.0
// guid: 333780a3-eba8-4978-9f00-8866eed4cfa7
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../../test/renderWithProviders';
import { loginPageResponse } from '../../test/loginRedirect';
import DelugeSettingsTab from './DelugeSettingsTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });

describe('DelugeSettingsTab', () => {
  it('does not report a connection when the test call is answered with a login page', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url.endsWith('/deluge/status')) return json({ configured: true, url: 'http://deluge.invalid' });
        return loginPageResponse();
      })
    );
    const user = userEvent.setup();
    renderWithProviders(<DelugeSettingsTab />);

    const button = screen.getByRole('button', { name: /test connection/i });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);

    expect(await screen.findByText(/connection failed/i)).toBeInTheDocument();
    expect(screen.queryByText(/connected to deluge successfully/i)).not.toBeInTheDocument();
  });

  it('does not show an import result when the import call is answered with a login page', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    const user = userEvent.setup();
    renderWithProviders(<DelugeSettingsTab />);

    await user.click(screen.getByRole('button', { name: /import unimported/i }));

    await waitFor(() =>
      expect(screen.getByRole('button', { name: /import unimported/i })).toBeEnabled()
    );
    expect(screen.queryByText(/Imported:/)).not.toBeInTheDocument();
  });
});
