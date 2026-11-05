// file: web/src/pages/deepLinks.tabs.test.tsx
// version: 1.1.0
// guid: 91c3e5a8-0b6d-4f27-8a49-2d7e1f5c3b60
// last-edited: 2026-11-05

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useLocation, useNavigate } from 'react-router-dom';
import { renderWithProviders } from '../test/renderWithProviders';

vi.mock('../services/playlistApi', () => ({
  listPlaylists: vi.fn(),
  createPlaylist: vi.fn(),
  deletePlaylist: vi.fn(),
}));
vi.mock('../services/collectionApi', async (orig) => ({
  ...(await orig<typeof import('../services/collectionApi')>()),
  listCollections: vi.fn(),
  createCollection: vi.fn(),
  deleteCollection: vi.fn(),
}));
vi.mock('../contexts/AuthContext', () => ({
  useAuth: () => ({
    initialized: true,
    authEnabled: true,
    user: { id: 'u1', permissions: ['library.view', 'collections.manage'] },
  }),
}));
vi.mock('../components/AIJobsPanel', () => ({ AIJobsPanel: () => null }));
vi.mock('../components/CacheStatsPanel', () => ({ CacheStatsPanel: () => null }));
vi.mock('../components/common/ServerFileBrowser', () => ({
  ServerFileBrowser: ({
    initialPath,
    onSelect,
  }: {
    initialPath: string;
    onSelect: (p: string, d: boolean) => void;
  }) => (
    <div>
      <div data-testid="browser-start">{initialPath}</div>
      <button onClick={() => onSelect('/audiobooks/gotham', true)}>open gotham</button>
      <button onClick={() => onSelect('/', true)}>open root</button>
    </div>
  ),
}));

import { listPlaylists } from '../services/playlistApi';
import { listCollections } from '../services/collectionApi';
import Playlists from './Playlists';
import Collections from './Collections';
import TrashedVersions from './TrashedVersions';
import { Diagnostics } from './Diagnostics';
import { FileBrowser } from './FileBrowser';

function Probe() {
  const { pathname, search } = useLocation();
  return <div data-testid="loc">{pathname + search}</div>;
}
function Linker({ to }: { to: string }) {
  const navigate = useNavigate();
  return <button onClick={() => navigate(to)}>link:{to}</button>;
}
const loc = () => screen.getByTestId('loc').textContent;
// Let every effect and the mirror's trailing write finish before a count is read:
// a duplicate fetch caused by the URL echo arrives after the click, not with it.
const settle = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 500));
  });

beforeEach(() => {
  vi.mocked(listPlaylists).mockResolvedValue({ playlists: [], count: 0 });
  vi.mocked(listCollections).mockResolvedValue({ collections: [], count: 0 });
});
afterEach(() => {
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe('/playlists tab', () => {
  it('?tab=smart seeds the tab and ONE fetch asks for smart playlists', async () => {
    renderWithProviders(
      <>
        <Playlists />
        <Probe />
      </>,
      { initialEntries: ['/playlists?tab=smart'] }
    );
    expect(screen.getByRole('tab', { name: 'Smart' })).toHaveAttribute('aria-selected', 'true');
    await waitFor(() => expect(listPlaylists).toHaveBeenCalled());
    await settle();
    expect(listPlaylists).toHaveBeenCalledTimes(1);
    expect(listPlaylists).toHaveBeenCalledWith('smart');
    expect(loc()).toBe('/playlists?tab=smart');
  });

  it('a click writes ?tab= with one fetch, and back to All leaves the URL bare', async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <>
        <Playlists />
        <Probe />
      </>,
      { initialEntries: ['/playlists'] }
    );
    await waitFor(() => expect(listPlaylists).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole('tab', { name: 'Static' }));
    await waitFor(() => expect(loc()).toBe('/playlists?tab=static'));
    await settle();
    // One fetch for the click (2 in all), not one more for the URL echo.
    expect(listPlaylists).toHaveBeenCalledTimes(2);
    expect(listPlaylists).toHaveBeenLastCalledWith('static');
    await user.click(screen.getByRole('tab', { name: 'All' }));
    await waitFor(() => expect(loc()).toBe('/playlists'));
    await settle();
    expect(listPlaylists).toHaveBeenCalledTimes(3);
  });

  it('a link to another tab while on the page re-seeds it', async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <>
        <Playlists />
        <Probe />
        <Linker to="/playlists?tab=smart" />
      </>,
      { initialEntries: ['/playlists'] }
    );
    await user.click(screen.getByText('link:/playlists?tab=smart'));
    expect(screen.getByRole('tab', { name: 'Smart' })).toHaveAttribute('aria-selected', 'true');
  });
});

describe('/collections tab', () => {
  it('?tab=dynamic seeds the tab and the type query', async () => {
    renderWithProviders(
      <>
        <Collections />
        <Probe />
      </>,
      { initialEntries: ['/collections?tab=dynamic'] }
    );
    expect(screen.getByRole('tab', { name: 'Dynamic' })).toHaveAttribute('aria-selected', 'true');
    await waitFor(() => expect(listCollections).toHaveBeenCalled());
    await settle();
    expect(listCollections).toHaveBeenCalledTimes(1);
    expect(vi.mocked(listCollections).mock.calls.at(0)?.[0]).toBe('dynamic');
    expect(loc()).toBe('/collections?tab=dynamic');
  });

  it('a click writes ?tab= with one fetch', async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <>
        <Collections />
        <Probe />
      </>,
      { initialEntries: ['/collections'] }
    );
    await waitFor(() => expect(listCollections).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole('tab', { name: 'Static' }));
    await waitFor(() => expect(loc()).toBe('/collections?tab=static'));
    await settle();
    expect(listCollections).toHaveBeenCalledTimes(2);
  });
});

describe('/versions tab', () => {
  function stubVersions() {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL) =>
        new Response(JSON.stringify({ data: { versions: [] } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
    );
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  it('?tab=purged seeds the tab and reads the purged list once', async () => {
    const fetchMock = stubVersions();
    renderWithProviders(
      <>
        <TrashedVersions />
        <Probe />
      </>,
      { initialEntries: ['/versions?tab=purged'] }
    );
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(String(fetchMock.mock.calls[0][0])).toContain('purged-versions');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('a click writes ?tab=; a cleanup link with ?set= implies its tab without one', async () => {
    const user = userEvent.setup();
    stubVersions();
    renderWithProviders(
      <>
        <TrashedVersions />
        <Probe />
      </>,
      { initialEntries: ['/versions'] }
    );
    await user.click(await screen.findByRole('tab', { name: /Purged/ }));
    expect(loc()).toBe('/versions?tab=purged');
    await user.click(screen.getByRole('tab', { name: /Trash/ }));
    expect(loc()).toBe('/versions');
  });
});

describe('/diagnostics category', () => {
  it('?category=dedup selects the card; a click writes the URL; an unknown id selects nothing', async () => {
    const user = userEvent.setup();
    const { unmount } = renderWithProviders(
      <>
        <Diagnostics />
        <Probe />
      </>,
      { initialEntries: ['/diagnostics?category=nonsense'] }
    );
    expect(screen.getByRole('button', { name: /Download ZIP/i })).toBeDisabled();
    unmount();
    renderWithProviders(
      <>
        <Diagnostics />
        <Probe />
      </>,
      { initialEntries: ['/diagnostics?category=dedup'] }
    );
    expect(screen.getByRole('button', { name: /Download ZIP/i })).toBeEnabled();
    await user.click(screen.getByText('Metadata Quality'));
    expect(loc()).toBe('/diagnostics?category=metadata');
  });
});

describe('/files path', () => {
  it('opens the browser at ?path= and writes each directory it lands on', async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <>
        <FileBrowser />
        <Probe />
      </>,
      { initialEntries: ['/files?path=/audiobooks/arkham'] }
    );
    expect(screen.getByTestId('browser-start').textContent).toBe('/audiobooks/arkham');
    await user.click(screen.getByText('open gotham'));
    expect(loc()).toBe('/files?path=%2Faudiobooks%2Fgotham');
    await act(async () => {
      await user.click(screen.getByText('open root'));
    });
    expect(loc()).toBe('/files');
  });

  it('follows a path the address bar changes under it (a link, back/forward)', async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <>
        <FileBrowser />
        <Probe />
        <Linker to="/files?path=/audiobooks/arkham" />
      </>,
      { initialEntries: ['/files'] }
    );
    expect(screen.getByTestId('browser-start').textContent).toBe('/');
    await user.click(screen.getByText('link:/files?path=/audiobooks/arkham'));
    await waitFor(() =>
      expect(screen.getByTestId('browser-start').textContent).toBe('/audiobooks/arkham')
    );
    // Our own write is not mistaken for an outside one: the browser is not reopened.
    await user.click(screen.getByText('open gotham'));
    await waitFor(() => expect(loc()).toBe('/files?path=%2Faudiobooks%2Fgotham'));
    await settle();
    expect(screen.getByTestId('browser-start').textContent).toBe('/audiobooks/arkham');
  });
});
