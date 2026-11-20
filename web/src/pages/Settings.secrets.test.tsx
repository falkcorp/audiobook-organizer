// file: web/src/pages/Settings.secrets.test.tsx
// version: 1.0.0
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

function makeView(opts: { keySet: boolean; keyLock?: string; logLevel?: string }): ConfigView {
  return {
    generation: 1,
    naming_pattern_tokens: ['{title}'],
    keys: [
      { name: 'root_dir', class: 'i', value: '/library/gotham', admin: true },
      { name: 'playlist_dir', class: 'k', value: '/library/gotham/playlists' },
      { name: 'database_path', class: 'k', value: '/var/lib/abo/db', locked_by: 'flag --db' },
      { name: 'log_level', class: 'k', value: opts.logLevel ?? 'info' },
      { name: 'enable_ai_parsing', class: 'k', value: true },
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

  beforeEach(() => {
    puts = [];
    view = makeView({ keySet: true });
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: unknown, init?: RequestInit) => {
        const u = String(url);
        if (u.endsWith('/config') && init?.method === 'PUT') {
          const body = JSON.parse(String(init.body));
          puts.push(body);
          if (body.unset?.includes('openai_api_key')) {
            view = makeView({ keySet: false });
          } else if (body.set?.openai_api_key) {
            view = makeView({ keySet: true });
          }
          return new Response(JSON.stringify({ data: { ...view, applied: Object.keys(body.set ?? {}) } }), {
            status: 200,
          });
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
    for (const served of ['root_dir', 'playlist_dir', 'database_path', 'log_level', 'enable_ai_parsing']) {
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
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Clear key' })).not.toBeInTheDocument());
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
});
