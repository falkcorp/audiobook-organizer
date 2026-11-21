// file: web/src/components/uiKitWrappers.test.tsx
// version: 0.1.0
// guid: ce305731-acb4-4aa5-a38e-b2ec69b167e9
// last-edited: 2026-11-21

// F9-05: the old web paths of the components that moved to @mediacore/ui-kit.
// Three are plain re-exports. Four are thin wrappers that supply what ui-kit no
// longer knows: ABO's shortcut table, its announcements endpoint and dismissed
// key, its table_config_ prefix and its /api/v1 filesystem calls. Each wrapper
// is checked for exactly that, since the ui-kit tests cannot see it.

import { act, fireEvent, render, renderHook, screen, waitFor } from '@testing-library/react';
import { useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as uiKit from '@mediacore/ui-kit';
import { renderWithProviders } from '../test/renderWithProviders';
import { STORAGE_KEYS } from '../lib/storageKeys';
import * as api from '../services/api';
import { AnnouncementBanner } from './AnnouncementBanner';
import { BulkConfirmDialog } from './common/BulkConfirmDialog';
import { ColumnPicker, ResizableHeaderCell, useConfigurableTable, type ColumnDef } from './common/ConfigurableTable';
import { ErrorBoundary } from './ErrorBoundary';
import { FilterTagBar } from './common/FilterTagBar';
import { KeyboardShortcutsDialog } from './KeyboardShortcutsDialog';
import { ServerFileBrowser } from './common/ServerFileBrowser';

vi.mock('../services/api', async (orig) => ({
  ...(await orig<typeof import('../services/api')>()),
  browseFilesystem: vi.fn(),
  excludeFilesystemPath: vi.fn(),
  includeFilesystemPath: vi.fn(),
}));

beforeEach(() => localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe('plain re-exports', () => {
  it('are the ui-kit components', () => {
    expect(BulkConfirmDialog).toBe(uiKit.BulkConfirmDialog);
    expect(FilterTagBar).toBe(uiKit.FilterTagBar);
    expect(ErrorBoundary).toBe(uiKit.ErrorBoundary);
    expect(ColumnPicker).toBe(uiKit.ColumnPicker);
    expect(ResizableHeaderCell).toBe(uiKit.ResizableHeaderCell);
  });
});

describe('KeyboardShortcutsDialog wrapper', () => {
  it("shows ABO's shortcut table", () => {
    renderWithProviders(<KeyboardShortcutsDialog open onClose={() => {}} />);
    expect(screen.getByText('Go to Library')).toBeInTheDocument();
    expect(screen.getByText('Go to Settings')).toBeInTheDocument();
  });
});

describe('useConfigurableTable wrapper', () => {
  const cols: ColumnDef<{ n: string }>[] = [{ key: 'n', label: 'N', render: (r) => r.n }];

  it('keeps the legacy table_config_<name> key', () => {
    const { result } = renderHook(() => useConfigurableTable({ storageKey: 'villains', columns: cols }));
    act(() => result.current.resetColumns());
    expect(STORAGE_KEYS.TABLE_CONFIG).toBe('table_config');
    expect(localStorage.getItem('table_config_villains')).not.toBeNull();
    expect(localStorage.getItem('villains')).toBeNull();
  });

  it('reads a layout a pre-split build stored', () => {
    const two: ColumnDef<{ n: string }>[] = [...cols, { key: 'm', label: 'M', render: () => '' }];
    localStorage.setItem('table_config_villains', JSON.stringify({ visible: ['m'], widths: { m: 70, n: 90 } }));
    const { result } = renderHook(() => useConfigurableTable({ storageKey: 'villains', columns: two }));
    expect(result.current.visibleColumns.map((c) => c.key)).toEqual(['m']);
  });
});

describe('AnnouncementBanner wrapper', () => {
  function stubAnnouncements(link?: string) {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL) =>
        new Response(JSON.stringify({ data: { announcements: [{ id: 'a1', severity: 'info', message: 'Gotham maintenance tonight', link }] } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  it('fetches from the legacy endpoint and dismisses under the legacy key', async () => {
    const fetchMock = stubAnnouncements();
    renderWithProviders(<AnnouncementBanner />);
    expect(await screen.findByText('Gotham maintenance tonight')).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0][0])).toContain('/api/v1/system/announcements');
    fireEvent.click(screen.getByLabelText('Dismiss'));
    expect(JSON.parse(localStorage.getItem('dismissed_announcements')!)).toEqual(['a1']);
  });

  it('stays hidden for an id dismissed by a pre-split build', async () => {
    localStorage.setItem('dismissed_announcements', JSON.stringify(['a1']));
    const fetchMock = stubAnnouncements();
    const { container } = renderWithProviders(<AnnouncementBanner />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it('routes a banner link through the router', async () => {
    stubAnnouncements('/settings');
    function Where() {
      return <p data-testid="where">{useLocation().pathname}</p>;
    }
    renderWithProviders(
      <>
        <AnnouncementBanner />
        <Where />
      </>,
    );
    expect(screen.getByTestId('where').textContent).toBe('/');
    fireEvent.click(await screen.findByText('Gotham maintenance tonight'));
    expect(screen.getByTestId('where').textContent).toBe('/settings');
  });
});

describe('ServerFileBrowser wrapper', () => {
  const listing = {
    path: '/wayne',
    count: 2,
    items: [
      { name: 'cave', path: '/wayne/cave', is_dir: true, excluded: false },
      { name: 'attic', path: '/wayne/attic', is_dir: true, excluded: true },
    ],
  };

  beforeEach(() => {
    vi.mocked(api.browseFilesystem).mockReset().mockResolvedValue(listing);
    vi.mocked(api.excludeFilesystemPath).mockReset().mockResolvedValue({ excluded: true, path: '/wayne/cave' });
    vi.mocked(api.includeFilesystemPath).mockReset().mockResolvedValue(undefined);
  });

  it('browses through the legacy api', async () => {
    render(<ServerFileBrowser initialPath="/wayne" />);
    await screen.findByText('cave');
    expect(api.browseFilesystem).toHaveBeenCalledWith('/wayne');
  });

  it('excludes and includes through the legacy api from the context menu', async () => {
    render(<ServerFileBrowser initialPath="/wayne" />);
    await screen.findByText('cave');
    fireEvent.contextMenu(screen.getByText('cave'));
    fireEvent.click(await screen.findByText('Exclude from scan'));
    await waitFor(() => expect(api.excludeFilesystemPath).toHaveBeenCalledWith('/wayne/cave'));
    fireEvent.contextMenu(screen.getByText('attic'));
    fireEvent.click(await screen.findByText('Include in scan'));
    await waitFor(() => expect(api.includeFilesystemPath).toHaveBeenCalledWith('/wayne/attic'));
  });
});
