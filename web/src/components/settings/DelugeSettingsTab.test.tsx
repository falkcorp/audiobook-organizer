// file: web/src/components/settings/DelugeSettingsTab.test.tsx
// version: 1.4.0
// guid: 333780a3-eba8-4978-9f00-8866eed4cfa7
// last-edited: 2026-10-11

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../../test/renderWithProviders';
import { loginPageWithJsonBody } from '../../test/loginRedirect';
import DelugeSettingsTab from './DelugeSettingsTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

// The server wraps every success as { data: ... } and every failure as
// { error, code, status }.
const envelope = (data: unknown, status = 200) =>
  new Response(JSON.stringify({ data }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
const failure = (error: string, status: number) =>
  new Response(JSON.stringify({ error, status }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const configured = envelope({ configured: true, url: 'http://deluge.invalid' });

describe('DelugeSettingsTab', () => {
  it('reads the configured state from the { data } envelope', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => configured.clone()));
    renderWithProviders(<DelugeSettingsTab />);
    expect(await screen.findByText('Configured')).toBeInTheDocument();
  });

  it('does not report a connection when the test call is answered with a login page', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/deluge/status')
          ? configured.clone()
          : loginPageWithJsonBody({ data: { connected: true } })
      )
    );
    const user = userEvent.setup();
    renderWithProviders(<DelugeSettingsTab />);

    const button = screen.getByRole('button', { name: /test connection/i });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);

    expect(await screen.findByText(/connection failed: .*session has expired/i)).toBeInTheDocument();
    expect(screen.queryByText(/connected to deluge successfully/i)).not.toBeInTheDocument();
  });

  it('shows the server reason when the test call fails with a 502', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/deluge/status') ? configured.clone() : failure('login refused', 502)
      )
    );
    const user = userEvent.setup();
    renderWithProviders(<DelugeSettingsTab />);

    const button = screen.getByRole('button', { name: /test connection/i });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);

    expect(await screen.findByText(/connection failed: login refused/i)).toBeInTheDocument();
  });

  it('shows an error when the status call fails', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => failure('db down', 500)));
    renderWithProviders(<DelugeSettingsTab />);
    expect(await screen.findByText(/failed to load deluge status/i)).toBeInTheDocument();
  });

  it('shows an error when the torrents call fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/deluge/status') ? configured.clone() : failure('no client', 502)
      )
    );
    const user = userEvent.setup();
    renderWithProviders(<DelugeSettingsTab />);

    const button = screen.getByRole('button', { name: /view torrents/i });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);

    expect(await screen.findByText(/failed to load torrents: no client/i)).toBeInTheDocument();
    expect(screen.queryByText(/^Torrents \(/)).not.toBeInTheDocument();
  });

  it('shows an error, not a result, when the import call is answered with a login page', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/deluge/status')
          ? configured.clone()
          : loginPageWithJsonBody({ data: { total: 3, imported: 3, failed: 0 } })
      )
    );
    const user = userEvent.setup();
    renderWithProviders(<DelugeSettingsTab />);

    await user.click(screen.getByRole('button', { name: /import unimported/i }));

    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
    expect(screen.queryByText(/Imported:/)).not.toBeInTheDocument();
  });

  it('shows the import counts from the { data } envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/discovery/import')
          ? envelope({ total: 3, imported: 2, failed: 1 })
          : configured.clone()
      )
    );
    const user = userEvent.setup();
    renderWithProviders(<DelugeSettingsTab />);

    await user.click(screen.getByRole('button', { name: /import unimported/i }));
    expect(await screen.findByText(/Total: 3/)).toBeInTheDocument();
  });

  describe('locked settings', () => {
    it('shows no lock notice and the unlocked help text when nothing is locked', async () => {
      vi.stubGlobal('fetch', vi.fn(async () => configured.clone()));
      renderWithProviders(<DelugeSettingsTab envLocked={[]} settingLocks={{}} />);
      await screen.findByText('Configured');

      expect(screen.queryByText(/set by the server and override/i)).not.toBeInTheDocument();
      expect(screen.getByText(/overrides the saved one and locks this setting/i)).toBeInTheDocument();
      expect(screen.queryByText(/^Locked:/)).not.toBeInTheDocument();
    });

    it('names the environment variable that locks the URL and keeps the field read-only', async () => {
      vi.stubGlobal('fetch', vi.fn(async () => configured.clone()));
      renderWithProviders(
        <DelugeSettingsTab
          envLocked={['deluge_web_url']}
          settingLocks={{ deluge_web_url: 'DELUGE_WEB_URL' }}
        />
      );
      await screen.findByText('Configured');

      expect(screen.getByLabelText('Deluge Web URL')).toBeDisabled();
      expect(screen.getByText(/set by the server and override/i)).toBeInTheDocument();
      expect(
        screen.getByText(/the DELUGE_WEB_URL environment variable on the server/i, {
          selector: 'li p',
          exact: false,
        })
      ).toBeInTheDocument();
      expect(screen.getByText(/^Locked: set by the DELUGE_WEB_URL environment variable/)).toBeInTheDocument();
    });

    it('says the config file is the source when it locks a setting', async () => {
      vi.stubGlobal('fetch', vi.fn(async () => configured.clone()));
      renderWithProviders(
        <DelugeSettingsTab
          envLocked={['deluge_web_url', 'deluge_move_enabled']}
          settingLocks={{
            deluge_web_url: 'config file /etc/audiobook-organizer/config.example.yaml',
            deluge_move_enabled: 'DELUGE_MOVE_ENABLED',
          }}
        />
      );
      await screen.findByText('Configured');

      // The note names the file the server loaded, so the operator edits the right one.
      expect(
        screen.getByText(
          /^Locked: set by the server's config file \(\/etc\/audiobook-organizer\/config\.example\.yaml\)/
        )
      ).toBeInTheDocument();
      expect(screen.getByText('Move storage enabled')).toBeInTheDocument();
      expect(screen.queryByText('Discovery label')).not.toBeInTheDocument();
    });

    it('falls back to a bare config file reason when the server did not name the file', async () => {
      vi.stubGlobal('fetch', vi.fn(async () => configured.clone()));
      renderWithProviders(
        <DelugeSettingsTab
          envLocked={['deluge_web_url']}
          settingLocks={{ deluge_web_url: 'config file' }}
        />
      );
      await screen.findByText('Configured');

      expect(screen.getByText(/^Locked: set by the server's config file,/)).toBeInTheDocument();
    });

    it('lists the password lock by name and never renders a value for it', async () => {
      vi.stubGlobal('fetch', vi.fn(async () => configured.clone()));
      renderWithProviders(
        <DelugeSettingsTab
          envLocked={['deluge_web_password']}
          settingLocks={{ deluge_web_password: 'DELUGE_WEB_PASSWORD' }}
        />
      );
      await screen.findByText('Configured');

      expect(screen.getByText('Web password')).toBeInTheDocument();
      expect(screen.getByText(/DELUGE_WEB_PASSWORD environment variable/)).toBeInTheDocument();
      // The URL is not locked here, so its help text stays the unlocked one.
      expect(screen.queryByText(/^Locked:/)).not.toBeInTheDocument();
    });
  });
});
