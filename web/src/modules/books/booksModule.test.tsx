// file: web/src/modules/books/booksModule.test.tsx
// version: 0.1.0
// guid: 8fd3ba31-18f6-4b30-9474-30099201b685
// last-edited: 2026-11-22

// F9-03 prep (A3): booksModule mounted on the real createShell in jsdom, against a mocked
// core API. Nothing in the running app imports booksModule yet; this test is its only importer.
// The page modules are stubbed (the moved pages need ABO's own providers), but through the
// module's own `lazy` entries, so a wrong import path or export name fails here.

import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createShell } from '@mediacore/shell';
import { booksNavItems } from './layout/nav';
import { booksManifest } from './manifest';
import { BOOKS_MODULE_MARKER, booksModule } from './module';

vi.mock('../../pages/Library', () => ({
  Library: ({ defaultPreset }: { defaultPreset?: string }) => <div>Library page, preset {defaultPreset}</div>,
}));
vi.mock('../../pages/Series', () => ({ Series: () => <div>Series page</div> }));
vi.mock('../../pages/BookDetail', () => ({ BookDetail: () => <div>Book detail page</div> }));
vi.mock('../../pages/AuthorDetail', () => ({ default: () => <div>Author detail page</div> }));
// The banner slot and the top-bar search poll ABO's stores; the nav is what is under test.
vi.mock('./layout/banners', () => ({ BooksBanners: () => <div>Books banners</div> }));
vi.mock('./layout/LibrarySearch', () => ({ LibrarySearch: () => <div>Library search</div> }));

// Defined by vitest.config.ts (the shell reads it); tsc needs the declaration.
declare const MEDIACORE_CORE_VERSION: string;

// The entries the shell owns after the flip: books does not contribute them.
const SHELL_OWNED = new Set(['Dashboard', 'Activity', 'Diagnostics', 'System', 'Users', 'Settings']);

function setMeta(name: string, content: string) {
  const m = document.createElement('meta');
  m.setAttribute('name', name);
  m.setAttribute('content', content);
  document.head.appendChild(m);
}

/** The /api/core/v1 answers a signed-in page needs, with books' manifest as the server's declaration. */
function coreApi() {
  return vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const json = (body: unknown, status = 200) =>
      new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    if (url.endsWith('/api/core/v1/auth/status')) return json({ has_users: true, auth_disabled: false, setup_required: false });
    if (url.endsWith('/api/core/v1/me'))
      return json({ user: { id: 'wayne-1', username: 'bruce' }, permissions: ['core:admin'], method: 'session', owner_verified: true });
    if (url.endsWith('/api/core/v1/modules'))
      return json({
        core: { version: MEDIACORE_CORE_VERSION },
        app: { id: 'batcave', display_name: 'Batcave', version: '1', ui_build_id: '' },
        restart_required: false,
        modules: [
          {
            id: booksManifest.id,
            version: booksManifest.version,
            display_name: booksManifest.displayName,
            ui: {
              pages: [...booksManifest.pages],
              nav: [...booksManifest.nav],
              settings_panels: [...booksManifest.settingsPanels],
              widgets: [...booksManifest.widgets],
              banners: [...booksManifest.banners],
              top_bar_items: [...booksManifest.topBarItems],
              user_page: [...booksManifest.userPage],
              activity_panes: [...booksManifest.activityPanes],
              setup_steps: [...booksManifest.setupSteps],
            },
          },
        ],
      });
    if (url.endsWith('/api/core/v1/ui/diagnostics')) return new Response(null, { status: 204 });
    if (url.includes('/api/core/v1/events')) return new Promise<Response>(() => {});
    return json({ title: 'not found' }, 404);
  });
}

let shell: ReturnType<typeof createShell> | null = null;

function mountAt(path: string) {
  window.history.replaceState(null, '', path);
  const host = document.createElement('div');
  document.body.appendChild(host);
  shell = createShell({ app: { id: 'batcave', displayName: 'Batcave' }, modules: [booksModule] });
  act(() => shell!.mount(host));
}

beforeEach(() => {
  setMeta('mediacore-app', 'batcave');
  vi.stubGlobal('fetch', coreApi());
});

afterEach(() => {
  act(() => shell?.unmount());
  shell = null;
  document.head.querySelectorAll('meta[name="mediacore-app"]').forEach((m) => m.remove());
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

describe('booksModule on createShell', () => {
  it('is the module the dark proof looks for', () => {
    expect(BOOKS_MODULE_MARKER).toBe('books-module-defined-for-the-shell');
    expect(booksModule.id).toBe('books');
  });

  it('renders the books nav: the Library group with its children, then the plain entries, in booksNavItems order', async () => {
    mountAt('/books/series');
    expect(await screen.findByRole('button', { name: 'Library' })).toBeInTheDocument();
    const expected = booksNavItems.filter((i) => !SHELL_OWNED.has(i.text));
    for (const item of expected) {
      expect(screen.getByRole('button', { name: new RegExp(`^${item.text}`) })).toBeInTheDocument();
      for (const child of item.children ?? []) {
        expect(screen.getByRole('button', { name: child.text })).toBeInTheDocument();
      }
    }
    // The shell's own entries stand in for the ones books no longer lists.
    for (const owned of ['Activity', 'Settings']) {
      expect(screen.getByRole('button', { name: owned })).toBeInTheDocument();
    }
  });

  it('keeps every books nav entry and the Library selection rule', async () => {
    mountAt('/books/library');
    await screen.findByRole('button', { name: 'Library' });
    const flat = booksNavItems.flatMap((i) => [i, ...(i.children ?? [])]).filter((i) => !SHELL_OWNED.has(i.text) && i.text !== 'Library');
    const labels = booksModule.nav.flatMap((n) => [n, ...(n.children ?? [])]).map((n) => n.label);
    for (const item of flat) expect(labels).toContain(item.text);
    // All Books is selected on a plain visit; In Progress and Finished only with their search.
    expect(screen.getByRole('button', { name: 'All Books' })).toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'In Progress' })).not.toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'Finished' })).not.toHaveClass('Mui-selected');
  });

  it('selects In Progress, not All Books, on ?search=read_status:in_progress', async () => {
    mountAt('/books/library?search=read_status%3Ain_progress&page=1');
    expect(await screen.findByRole('button', { name: 'In Progress' })).toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'All Books' })).not.toHaveClass('Mui-selected');
  });

  it('navigates to the books page when a nav entry is clicked', async () => {
    mountAt('/books/series');
    fireEvent.click(await screen.findByRole('button', { name: 'Finished' }));
    await waitFor(() => expect(window.location.pathname).toBe('/books/library'));
    expect(window.location.search).toBe('?search=read_status:finished');
    expect(await screen.findByText('Library page, preset standard')).toBeInTheDocument();
  });

  it('resolves a books route to its page', async () => {
    mountAt('/books/series');
    expect(await screen.findByText('Series page')).toBeInTheDocument();
  });

  it('resolves the fingerprints route to the Library page with the fingerprints preset', async () => {
    mountAt('/books/fingerprints');
    expect(await screen.findByText('Library page, preset fingerprints')).toBeInTheDocument();
  });

  it('resolves param routes', async () => {
    mountAt('/books/library/book-1');
    expect(await screen.findByText('Book detail page')).toBeInTheDocument();
  });

  it('mounts the books banner and top-bar slots', async () => {
    mountAt('/books/series');
    expect(await screen.findByText('Books banners')).toBeInTheDocument();
    expect(await screen.findByText('Library search')).toBeInTheDocument();
  });

  it('sets the Review badge hook on the Review entry', () => {
    const review = booksModule.nav.find((n) => n.id === 'review');
    expect(review?.useBadge).toBe(booksNavItems.find((i) => i.text === 'Review')?.useBadge);
  });
});

describe('booksModule contributions match booksManifest', () => {
  const nav = (items: readonly { id: string; children?: readonly { id: string }[] }[]): string[] =>
    items.flatMap((n) => [n.id, ...nav(n.children ?? [])]);

  it('declares exactly the pages, nav entries, widgets, banners and user-page items it contributes', () => {
    expect(booksModule.routes.map((r) => r.id).sort()).toEqual([...booksManifest.pages].sort());
    expect(nav(booksModule.nav).sort()).toEqual([...booksManifest.nav].sort());
    expect((booksModule.widgets ?? []).map((w) => w.id).sort()).toEqual([...booksManifest.widgets].sort());
    expect((booksModule.banners ?? []).map((b) => b.id)).toEqual([...booksManifest.banners]);
    expect((booksModule.topBarItems ?? []).map((t) => t.id)).toEqual([...booksManifest.topBarItems]);
    const ext = booksModule.userPageExtensions;
    expect([...(ext?.rowActions ?? []), ...(ext?.panels ?? [])].map((x) => x.id).sort()).toEqual([...booksManifest.userPage].sort());
  });

  it('lists the same nav entries as booksNavItems minus the shell-owned ones', () => {
    const fromItems = booksNavItems
      .filter((i) => !SHELL_OWNED.has(i.text))
      .map((i) => [i.text, ...(i.children ?? []).map((c) => `  ${c.text}`)])
      .flat();
    const fromModule = booksModule.nav.flatMap((n) => [n.label, ...(n.children ?? []).map((c) => `  ${c.label}`)]);
    expect(fromModule).toEqual(fromItems);
  });
});
