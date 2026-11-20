// file: web/src/pages/Settings.secrets.test.tsx
// version: 1.2.0
// guid: 7b1d9e45-3c82-4a60-8f17-d4e0a6c2b593
// last-edited: 2026-11-19
//
// The Settings page over the key-shaped /config: a secret is shown as a status,
// never a value or a mask, is stored by typing and cleared with Clear, and a
// save names only the keys that changed.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import type { ConfigView } from '../services/configView';

vi.mock('../components/common/ServerFileBrowser', () => ({
  ServerFileBrowser: () => null,
}));
vi.mock('../components/system/SystemInfoTab', () => ({
  SystemInfoTab: () => null,
}));

import { Settings } from './Settings';

const TYPED_KEY = 'sk-riddler-typed-0007';

const SOURCES = (googleKeySaved: boolean) => [
  {
    id: 'audible',
    name: 'Audible',
    enabled: true,
    priority: 1,
    requires_auth: false,
    credentials: {},
  },
  {
    id: 'google-books',
    name: 'Google Books',
    enabled: true,
    priority: 2,
    requires_auth: true,
    // Served as the key with a blank value: presence means a key is saved.
    credentials: googleKeySaved ? { apiKey: '' } : {},
    base_url: 'https://books.mirror.example.test',
  },
];

function makeView(opts: {
  keySet: boolean;
  keyLock?: string;
  logLevel?: string;
  googleKeySaved?: boolean;
}): ConfigView {
  return {
    generation: 1,
    naming_pattern_tokens: ['{title}'],
    keys: [
      { name: 'root_dir', class: 'i', value: '/library/gotham', admin: true },
      { name: 'playlist_dir', class: 'k', value: '/library/gotham/playlists' },
      { name: 'database_path', class: 'k', value: '/var/lib/abo/db', locked_by: 'flag --db' },
      { name: 'log_level', class: 'k', value: opts.logLevel ?? 'info' },
      { name: 'enable_ai_parsing', class: 'k', value: true },
      { name: 'metadata_sources', class: 'i', value: SOURCES(opts.googleKeySaved ?? true) },
      {
        name: 'openai_api_key',
        class: 's',
        locked_by: opts.keyLock,
        secret: opts.keySet
          ? { set: true, source: opts.keyLock ? 'env' : 'stored', fingerprint: 'f00dbabe' }
          : { set: false },
      },
    ],
  };
}

describe('Settings secrets', () => {
  let view: ConfigView;
  let puts: Array<{ set: Record<string, unknown>; unset: string[] }>;
  // When set, the next PUT is refused with this status and body.
  let refusal: { status: number; body: Record<string, unknown> } | null;

  beforeEach(() => {
    puts = [];
    refusal = null;
    view = makeView({ keySet: true });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: unknown, init?: RequestInit) => {
        const u = String(url);
        if (u.endsWith('/config') && init?.method === 'PUT') {
          const body = JSON.parse(String(init.body));
          puts.push(body);
          if (refusal) {
            return new Response(JSON.stringify(refusal.body), { status: refusal.status });
          }
          if (body.unset?.includes('openai_api_key')) {
            view = makeView({ keySet: false });
          } else if (body.set?.openai_api_key) {
            view = makeView({ keySet: true });
          }
          const google = (
            body.set?.metadata_sources as
              Array<{ id: string; credentials: Record<string, unknown> }> | undefined
          )?.find((src) => src.id === 'google-books');
          if (google && 'apiKey' in google.credentials) {
            view = makeView({ keySet: true, googleKeySaved: google.credentials.apiKey !== null });
          }
          return new Response(
            JSON.stringify({ data: { ...view, applied: Object.keys(body.set ?? {}) } }),
            {
              status: 200,
            }
          );
        }
        if (u.endsWith('/config')) {
          return new Response(JSON.stringify({ data: view }), { status: 200 });
        }
        return new Response(JSON.stringify({ data: {} }), { status: 200 });
      })
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const keyField = async () => (await screen.findByLabelText('OpenAI API Key')) as HTMLInputElement;

  it('shows a saved key as status with its fingerprint, and never a value or a mask', async () => {
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
    const field = await keyField();
    await waitFor(() => expect(field.placeholder).toMatch(/Saved/));
    expect(field.value).toBe('');
    expect(field.type).toBe('password');
    expect(await screen.findByText(/fingerprint f00dbabe/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('***');
  });

  it('stores a typed key through set, and sends nothing else that did not change', async () => {
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
    const field = await keyField();
    await waitFor(() => expect(field.placeholder).toMatch(/Saved/));
    fireEvent.change(field, { target: { value: TYPED_KEY } });
    fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }));
    await waitFor(() => expect(puts).toHaveLength(1));

    expect(puts[0].set.openai_api_key).toBe(TYPED_KEY);
    expect(puts[0].unset).toEqual([]);
    // A served key whose value did not change stays out of the patch: the locked
    // database_path (the server refuses a locked key even with its own value) and
    // the rest.
    for (const served of [
      'root_dir',
      'playlist_dir',
      'database_path',
      'log_level',
      'enable_ai_parsing',
    ]) {
      expect(puts[0].set).not.toHaveProperty(served);
    }

    await waitFor(() => expect(field.value).toBe(''));
    expect(document.body.textContent).not.toContain(TYPED_KEY);
  });

  it('clears the key with unset after a confirmation, not by sending an empty value', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
    const field = await keyField();
    await waitFor(() => expect(field.placeholder).toMatch(/Saved/));
    fireEvent.click(await screen.findByRole('button', { name: 'Clear key' }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ set: {}, unset: ['openai_api_key'] });
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Clear key' })).not.toBeInTheDocument()
    );
  });

  it('does not clear when the confirmation is declined', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
    fireEvent.click(await screen.findByRole('button', { name: 'Clear key' }));
    expect(puts).toHaveLength(0);
  });

  it('locks the field and names the environment variable when the server supplies the key', async () => {
    view = makeView({ keySet: true, keyLock: 'env ABO_BOOKS_OPENAI_API_KEY' });
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
    const field = await keyField();
    await waitFor(() => expect(field).toBeDisabled());
    expect(screen.getByText(/ABO_BOOKS_OPENAI_API_KEY environment variable/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Clear key' })).not.toBeInTheDocument();
  });

  it('offers no Clear when no key is set', async () => {
    view = makeView({ keySet: false });
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
    const field = await keyField();
    await waitFor(() => expect(field.placeholder).toMatch(/Enter your OpenAI API key/));
    expect(screen.queryByRole('button', { name: 'Clear key' })).not.toBeInTheDocument();
    expect(within(document.body).queryByText(/fingerprint/)).not.toBeInTheDocument();
  });
  describe('metadata source credentials', () => {
    const googleField = async () =>
      (await screen.findByLabelText('Google Books API Key')) as HTMLInputElement;

    it('shows a saved source key as status and never serves a value into the field', async () => {
      renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
      const field = await googleField();
      await waitFor(() => expect(field.placeholder).toMatch(/Saved/));
      expect(field.value).toBe('');
      expect(screen.getByText(/A key is saved\. Type a new one/)).toBeInTheDocument();
    });

    it('a save that touches no credential leaves the sources out of the patch', async () => {
      renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
      const field = await googleField();
      await waitFor(() => expect(field.placeholder).toMatch(/Saved/));
      fireEvent.change(await keyField(), { target: { value: TYPED_KEY } });
      fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }));
      await waitFor(() => expect(puts).toHaveLength(1));
      // The list equals what the server served, so it is not named; if it were,
      // the saved credential would go as blank, which leaves it.
      expect(puts[0].set).not.toHaveProperty('metadata_sources');
    });

    it('sends a typed source key and keeps the fields the page has no control for', async () => {
      renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
      const field = await googleField();
      await waitFor(() => expect(field.placeholder).toMatch(/Saved/));
      fireEvent.change(field, { target: { value: 'sk-penguin-0011' } });
      fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }));
      await waitFor(() => expect(puts).toHaveLength(1));
      const sources = puts[0].set.metadata_sources as Array<Record<string, unknown>>;
      const google = sources.find((src) => src.id === 'google-books')!;
      expect(google.credentials).toEqual({ apiKey: 'sk-penguin-0011' });
      expect(google.base_url).toBe('https://books.mirror.example.test');
      await waitFor(() => expect(field.value).toBe(''));
      expect(document.body.textContent).not.toContain('sk-penguin-0011');
    });

    it('stages a clear and sends it as null on save', async () => {
      renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
      const field = await googleField();
      await waitFor(() => expect(field.placeholder).toMatch(/Saved/));
      fireEvent.click(screen.getByText('Clear saved key'));
      expect(screen.getByText(/will be deleted when you save/)).toBeInTheDocument();
      expect(puts).toHaveLength(0);
      fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }));
      await waitFor(() => expect(puts).toHaveLength(1));
      const sources = puts[0].set.metadata_sources as Array<{ id: string; credentials: unknown }>;
      expect(sources.find((src) => src.id === 'google-books')!.credentials).toEqual({
        apiKey: null,
      });
      await waitFor(() => expect(screen.queryByText('Clear saved key')).not.toBeInTheDocument());
    });

    it('offers no clear for a source with no saved key', async () => {
      view = makeView({ keySet: true, googleKeySaved: false });
      renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=metadata'] });
      const field = await googleField();
      await waitFor(() => expect(field.placeholder).toMatch(/Enter your Google Books API key/));
      expect(screen.queryByText('Clear saved key')).not.toBeInTheDocument();
    });
  });
  describe('a refused write of a library root', () => {
    const editLibraryPath = async () => {
      renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=library'] });
      const field = (await screen.findByLabelText('Library Path')) as HTMLInputElement;
      await waitFor(() => expect(field.value).toBe('/library/gotham'));
      fireEvent.change(field, { target: { value: '/library/arkham' } });
      fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }));
      await waitFor(() => expect(puts).toHaveLength(1));
      return field;
    };

    it('shows the 422 that names the field, keeps the typed path and does not report a save', async () => {
      const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {});
      refusal = {
        status: 422,
        body: {
          error: 'root_dir: must be an absolute path that exists — nothing was saved',
          code: 'config_invalid',
          fields: [{ key: 'root_dir', error: 'must be an absolute path that exists' }],
        },
      };
      const field = await editLibraryPath();
      // Only the keys that changed were named, root_dir among them.
      expect(puts[0].set.root_dir).toBe('/library/arkham');
      await waitFor(() => expect(alertSpy).toHaveBeenCalledTimes(1));
      const shown = String(alertSpy.mock.calls[0][0]);
      expect(shown).toContain('Failed to save settings');
      expect(shown).toContain('root_dir: must be an absolute path that exists');
      expect(field.value).toBe('/library/arkham');
      expect(screen.queryByText(/Settings saved successfully/i)).not.toBeInTheDocument();
    });

    it('shows the 403 that says the admin role is needed', async () => {
      const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => {});
      refusal = {
        status: 403,
        body: { error: 'changing root_dir needs the admin role', code: 'config_forbidden' },
      };
      await editLibraryPath();
      await waitFor(() => expect(alertSpy).toHaveBeenCalledTimes(1));
      expect(String(alertSpy.mock.calls[0][0])).toContain('changing root_dir needs the admin role');
    });
  });
});
