// file: web/src/components/settings/settingsSplit.test.tsx
// version: 0.1.0
// guid: 6107a187-b4c1-4c02-b3bd-07553e044af7
// last-edited: 2026-11-21

// Pins the Settings page as the running SPA renders it: the tab strip and its deep
// links, the config load, the dirty-state save bar and the save itself, the footer,
// the advanced switch and a few dialogs. F9-04 slice 6 splits the page into a shell
// frame and books panels without changing any of this; these tests are written
// first, against the untouched page, and passed unchanged through the cut. F3-07's
// key-shaped config wire (GET returns keys; a PUT names only the changed ones in
// `set`) changed the fetch stub and the save assertion, not the page behaviour.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { useLocation } from 'react-router';
import { renderWithProviders } from '../../test/renderWithProviders';

vi.mock('../common/ServerFileBrowser', () => ({ ServerFileBrowser: () => null }));
vi.mock('../system/SystemInfoTab', () => ({ SystemInfoTab: () => null }));

import { Settings } from '../../pages/Settings';

const TAB_LABELS = [
  'Library',
  'iTunes Import',
  'Metadata',
  'Dedup',
  'Paths',
  'Performance',
  'Security',
  'API Keys',
  'Plugins',
  'Tools',
  'System Info',
  'Temp Login',
];

const CONFIG = {
  root_dir: '/library/gotham-archive',
  supported_extensions: ['.m4b', '.mp3'],
  organization_strategy: 'auto',
};

// GET /config is key-shaped: one entry per key with its class (see services/configView.ts).
const viewOf = (values: Record<string, unknown>) => ({
  generation: 1,
  keys: Object.entries(values).map(([name, value]) => ({
    name,
    class: name === 'root_dir' ? 'i' : 'k',
    value,
  })),
});

const BACKUP = {
  filename: 'backup-year-one.tar.gz',
  size: 2048,
  created_at: '2026-11-01T00:00:00Z',
};

// The `set` half of each PUT /config body: the keys a save named.
let putBodies: Array<Record<string, unknown>> = [];

function Probe() {
  const location = useLocation();
  return <div data-testid="loc">{location.pathname + location.search + location.hash}</div>;
}

function renderSettings(entry = '/settings') {
  return renderWithProviders(
    <>
      <Settings />
      <Probe />
    </>,
    { initialEntries: [entry] }
  );
}

const json = (data: unknown) =>
  new Response(JSON.stringify({ data }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? 'GET').toUpperCase();
      if (url.includes('/config')) {
        if (method === 'PUT') {
          const body = JSON.parse(String(init?.body)) as { set?: Record<string, unknown> };
          putBodies.push(body.set ?? {});
          return json(viewOf({ ...CONFIG, ...body.set }));
        }
        return json(viewOf(CONFIG));
      }
      if (url.includes('/import-paths')) return json({ import_paths: [] });
      if (url.includes('/backup/list')) return json({ backups: [BACKUP], count: 1 });
      return json({});
    })
  );
}

// The Add button next to "Add extension" is the first of two (the exclude-pattern
// field has its own), in document order.
function addExtension(ext: string) {
  fireEvent.change(screen.getByLabelText('Add extension'), { target: { value: ext } });
  fireEvent.click(screen.getAllByRole('button', { name: 'Add' })[0]);
}

describe('Settings page', () => {
  beforeEach(() => {
    putBodies = [];
    localStorage.clear();
    stubFetch();
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('lists the twelve tabs in order with Library selected by default', async () => {
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    const tabs = within(screen.getByRole('tablist', { name: 'settings tabs' })).getAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(TAB_LABELS);
    expect(screen.getByRole('tab', { name: 'Library' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('heading', { name: 'Settings' })).toBeInTheDocument();
  });

  it('opens the tab named by ?tab= and falls back to the older #slug form', async () => {
    const first = renderSettings('/settings?tab=security');
    expect(
      await screen.findByRole('tab', { name: 'Security', selected: true })
    ).toBeInTheDocument();
    first.unmount();
    renderSettings('/settings#api-keys');
    expect(
      await screen.findByRole('tab', { name: 'API Keys', selected: true })
    ).toBeInTheDocument();
  });

  it('treats an unknown ?tab= as Library', async () => {
    renderSettings('/settings?tab=nope');
    expect(await screen.findByRole('tab', { name: 'Library', selected: true })).toBeInTheDocument();
  });

  it('writes the tab into the URL, and leaves Library (the default) out of it', async () => {
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    fireEvent.click(screen.getByRole('tab', { name: 'Dedup' }));
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/settings?tab=dedup'));
    fireEvent.click(screen.getByRole('tab', { name: 'Library' }));
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/settings'));
  });

  it('loads the server config into the Library tab and enables Save', async () => {
    renderSettings();
    expect(await screen.findByDisplayValue(CONFIG.root_dir)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save Settings' })).toBeEnabled();
    expect(await screen.findByText(BACKUP.filename)).toBeInTheDocument();
  });

  it('shows the footer actions', async () => {
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    for (const name of [
      'Export Settings',
      'Import Settings',
      'Reset to Defaults',
      'Save Settings',
    ]) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument();
    }
  });

  it('offers Discard and Save only while there are unsaved changes, and Discard reverts', async () => {
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    expect(screen.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument();

    addExtension('.flac');
    expect(await screen.findByText('.flac')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Discard' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    await waitFor(() => expect(screen.queryByText('.flac')).not.toBeInTheDocument());
    expect(screen.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument();
  });

  it('saves the edited settings with a PUT and reports success', async () => {
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    addExtension('.flac');
    await screen.findByText('.flac');

    fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }));
    expect(await screen.findByText('Settings saved successfully!')).toBeInTheDocument();
    expect(putBodies).toHaveLength(1);
    // A save names only the keys that moved: the untouched library root is not sent.
    expect(putBodies[0]).not.toHaveProperty('root_dir');
    expect(putBodies[0].supported_extensions).toEqual(['.flac', '.m4b', '.mp3']);
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument()
    );
  });

  it('refuses to save a blank library path and says why', async () => {
    renderSettings();
    const field = await screen.findByDisplayValue(CONFIG.root_dir);
    fireEvent.change(field, { target: { value: '   ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }));
    expect(await screen.findByText('Library path is required.')).toBeInTheDocument();
    expect(putBodies).toHaveLength(0);
  });

  it('puts the defaults back after Reset to Defaults is confirmed', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    fireEvent.click(screen.getByRole('button', { name: 'Reset to Defaults' }));
    expect(await screen.findByDisplayValue('/path/to/audiobooks/library')).toBeInTheDocument();
  });

  it('warns before unloading the page only while there are unsaved changes', async () => {
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    const clean = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);

    addExtension('.flac');
    await screen.findByRole('button', { name: 'Discard' });
    const dirty = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
  });

  it('keeps the advanced switch outside the save flow and stores it in the browser', async () => {
    renderSettings();
    await screen.findByDisplayValue(CONFIG.root_dir);
    const sw = screen.getByTestId('show-advanced-settings');
    expect(sw).not.toBeChecked();
    fireEvent.click(sw);
    expect(sw).toBeChecked();
    expect(localStorage.getItem('settings.showAdvanced')).toBe('true');
    expect(screen.queryByRole('button', { name: 'Discard' })).not.toBeInTheDocument();
  });

  it('opens the restore dialog for a listed backup', async () => {
    renderSettings();
    await screen.findByText(BACKUP.filename);
    fireEvent.click(screen.getByRole('button', { name: 'Restore' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Restore Backup')).toBeInTheDocument();
    expect(within(dialog).getByText(BACKUP.filename)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('opens and cancels the add-import-path dialog from the Paths tab', async () => {
    renderSettings('/settings?tab=paths');
    fireEvent.click(await screen.findByRole('button', { name: 'Add Import Path' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText('Add Import Path (Watch Location)')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Add Path' })).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText('Folder Path'), {
      target: { value: '/incoming/arkham' },
    });
    expect(within(dialog).getByRole('button', { name: 'Add Path' })).toBeEnabled();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });
});
