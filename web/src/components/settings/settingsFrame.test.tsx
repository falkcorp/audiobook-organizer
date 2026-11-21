// file: web/src/components/settings/settingsFrame.test.tsx
// version: 0.1.0
// guid: 8c16dbd9-aecd-429c-b7d7-b40eda7906bc
// last-edited: 2026-11-21

// The shell Settings frame on its own (fake panels, no books code), the books panel
// list, and the binding's tab slugs. The page-level behaviour is pinned by
// settingsSplit.test.tsx, written before the cut.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, renderHook, screen, waitFor } from '@testing-library/react';
import { useLocation } from 'react-router';
import { renderWithProviders } from '../../test/renderWithProviders';
import { SettingsFrame, type SettingsFramePanel } from './SettingsFrame';
import frameSource from './SettingsFrame.tsx?raw';
import { booksSettingsPanels } from '../../modules/books/pages/booksSettings';
import { useBooksSettings } from '../../modules/books/pages/booksSettingsContext';

vi.mock('../common/ServerFileBrowser', () => ({ ServerFileBrowser: () => null }));
vi.mock('../system/SystemInfoTab', () => ({ SystemInfoTab: () => null }));

import { Settings } from '../../pages/Settings';

const Riddler = () => <p>riddle me this</p>;
const Penguin = () => <p>umbrella inventory</p>;
const Joker = () => <p>joker panel</p>;

const PANELS: SettingsFramePanel[] = [
  { id: 'riddler', label: 'Riddler', Component: Riddler },
  { id: 'penguin', label: 'Penguin', Component: Penguin },
  { id: 'joker', label: 'Joker', Component: Joker },
];

function Probe() {
  const l = useLocation();
  return <div data-testid="loc">{l.pathname + l.search + l.hash}</div>;
}

describe('SettingsFrame', () => {
  beforeEach(() => localStorage.clear());

  it('shows the first panel by default and mounts only the active panel', () => {
    renderWithProviders(<SettingsFrame panels={PANELS} />);
    expect(screen.getByText('riddle me this')).toBeInTheDocument();
    expect(screen.queryByText('umbrella inventory')).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Riddler' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('heading', { name: 'Settings' })).toBeInTheDocument();
  });

  it('takes its tab ids from the panels it is given, ?tab= first and #id second', () => {
    const first = renderWithProviders(<SettingsFrame panels={PANELS} />, {
      initialEntries: ['/x?tab=penguin'],
    });
    expect(screen.getByText('umbrella inventory')).toBeInTheDocument();
    first.unmount();
    renderWithProviders(<SettingsFrame panels={PANELS} />, { initialEntries: ['/x#joker'] });
    expect(screen.getByText('joker panel')).toBeInTheDocument();
  });

  it('prefers ?tab= over #id and falls back to the first panel for an unknown id', () => {
    const first = renderWithProviders(<SettingsFrame panels={PANELS} />, {
      initialEntries: ['/x?tab=penguin#joker'],
    });
    expect(screen.getByText('umbrella inventory')).toBeInTheDocument();
    first.unmount();
    renderWithProviders(<SettingsFrame panels={PANELS} />, { initialEntries: ['/x?tab=library'] });
    expect(screen.getByText('riddle me this')).toBeInTheDocument();
  });

  it('writes the chosen tab to the URL and drops it for the first panel, and the old hash with it', async () => {
    renderWithProviders(
      <>
        <SettingsFrame panels={PANELS} />
        <Probe />
      </>,
      { initialEntries: ['/x#joker'] }
    );
    fireEvent.click(screen.getByRole('tab', { name: 'Penguin' }));
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/x?tab=penguin'));
    fireEvent.click(screen.getByRole('tab', { name: 'Riddler' }));
    await waitFor(() => expect(screen.getByTestId('loc').textContent).toBe('/x'));
  });

  it('renders the banners above the tabs and the footer inside the panel frame', () => {
    renderWithProviders(
      <SettingsFrame
        panels={PANELS}
        title="Gotham settings"
        banners={<p>saved at the Batcave</p>}
        footer={<p>sticky footer</p>}
      />
    );
    expect(screen.getByRole('heading', { name: 'Gotham settings' })).toBeInTheDocument();
    const banner = screen.getByText('saved at the Batcave');
    const tablist = screen.getByRole('tablist');
    const footer = screen.getByText('sticky footer');
    expect(banner.compareDocumentPosition(tablist) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(tablist.compareDocumentPosition(footer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(footer.closest('.MuiPaper-root')).toContainElement(tablist);
  });

  it('stores the advanced switch in this browser', () => {
    renderWithProviders(<SettingsFrame panels={PANELS} />);
    const sw = screen.getByTestId('show-advanced-settings');
    fireEvent.click(sw);
    expect(localStorage.getItem('settings.showAdvanced')).toBe('true');
  });

  it('imports no books code and no services/api', () => {
    expect(frameSource).not.toMatch(/modules\/books/);
    expect(frameSource).not.toMatch(/services\/api/);
    expect(frameSource).not.toMatch(/useSettingsHandlers|Settings\.tsx|pages\/Settings/);
  });
});

describe('books settings panels', () => {
  it('lists nine panels whose ids and labels are the tab slugs the page has always used', () => {
    expect(Object.values(booksSettingsPanels).map((p) => [p.id, p.label])).toEqual([
      ['library', 'Library'],
      ['itunes', 'iTunes Import'],
      ['metadata', 'Metadata'],
      ['dedup', 'Dedup'],
      ['paths', 'Paths'],
      ['performance', 'Performance'],
      ['security', 'Security'],
      ['tools', 'Tools'],
      ['system', 'System Info'],
    ]);
    for (const [key, panel] of Object.entries(booksSettingsPanels)) expect(panel.id).toBe(key);
  });

  it('refuses to read the settings outside the provider', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    expect(() => renderHook(() => useBooksSettings())).toThrow(/BooksSettingsProvider/);
    spy.mockRestore();
  });
});

describe('Settings binding', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        // getTools() reads a bare array; the config answer carries the nested object.
        if (url.endsWith('/tools')) return new Response('[]', { status: 200 });
        if (url.includes('/config')) {
          return new Response(JSON.stringify({ data: { config: {} } }), { status: 200 });
        }
        return new Response(JSON.stringify({ data: {} }), { status: 200 });
      })
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it.each([
    ['library', 'Library'],
    ['itunes', 'iTunes Import'],
    ['metadata', 'Metadata'],
    ['dedup', 'Dedup'],
    ['paths', 'Paths'],
    ['performance', 'Performance'],
    ['security', 'Security'],
    ['api-keys', 'API Keys'],
    ['plugins', 'Plugins'],
    ['tools', 'Tools'],
    ['system', 'System Info'],
    ['temp-login', 'Temp Login'],
  ])('?tab=%s selects the %s tab', async (slug, label) => {
    renderWithProviders(<Settings />, { initialEntries: [`/settings?tab=${slug}`] });
    expect(await screen.findByRole('tab', { name: label, selected: true })).toBeInTheDocument();
    expect(screen.getAllByRole('tabpanel').filter((p) => !p.hidden)).toHaveLength(1);
  });
});
