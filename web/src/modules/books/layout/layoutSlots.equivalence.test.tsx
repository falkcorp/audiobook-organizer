// file: web/src/modules/books/layout/layoutSlots.equivalence.test.tsx
// version: 0.1.0
// guid: 8b2d6f14-3c97-4e50-a1d8-6f0c9e4b2a73
// last-edited: 2026-11-21

// F9-02: the SPA's layout renders the same before and after its audiobook
// imports are cut into slots. The expected lists below are written out by hand
// (labels, order, the path each entry navigates to), taken from the Sidebar as
// it was before the cut, so a regenerated snapshot cannot hide a change.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../../test/renderWithProviders';
import { Sidebar } from '../../../components/layout/Sidebar';
import { useReviewStore } from '../../../stores/useReviewStore';

const navigateSpy = vi.fn();
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigateSpy };
});

// Every entry the expanded sidebar shows, top to bottom, with the path a click
// navigates to. Library sits second and owns the six sub-entries after it.
const EXPANDED: ReadonlyArray<readonly [label: string, path: string]> = [
  ['Dashboard', '/dashboard'],
  ['Library', '/library'],
  ['All Books', '/library?reset=1'],
  ['In Progress', '/library?search=read_status:in_progress'],
  ['Finished', '/library?search=read_status:finished'],
  ['Fingerprints', '/fingerprints'],
  ['Series', '/series'],
  ['Authors', '/authors'],
  ['File Browser', '/files'],
  ['Works', '/works'],
  ['Playlists', '/playlists'],
  ['Collections', '/collections'],
  ['Activity', '/activity'],
  ['Review', '/review'],
  ['Diagnostics', '/diagnostics'],
  ['System', '/system'],
  ['Users', '/users'],
  ['Settings', '/settings'],
];

// The collapsed sidebar: icon-only buttons named by their tooltips, with the
// Library sub-entries behind the Library button's menu.
const COLLAPSED_TOP: readonly string[] = [
  'Dashboard',
  'Library',
  'File Browser',
  'Works',
  'Playlists',
  'Collections',
  'Activity',
  'Review',
  'Diagnostics',
  'System',
  'Users',
  'Settings',
];
const LIBRARY_MENU: ReadonlyArray<readonly [label: string, path: string]> = [
  ['All Books', '/library?reset=1'],
  ['In Progress', '/library?search=read_status:in_progress'],
  ['Finished', '/library?search=read_status:finished'],
  ['Fingerprints', '/fingerprints'],
  ['Series', '/series'],
  ['Authors', '/authors'],
];

function setReviewCount(count: number) {
  useReviewStore.setState({ count, byKind: {}, items: [], itemsLoading: false, _pollTimer: null });
}

// open={false}: the mobile temporary drawer stays closed (hidden from the
// accessibility tree), so only the permanent drawer's buttons are queried; see
// Sidebar.test.tsx for why.
function renderSidebar(opts: { collapsed: boolean; at?: string }) {
  return renderWithProviders(
    <Sidebar open={false} onClose={vi.fn()} drawerWidth={240} collapsed={opts.collapsed} />,
    { initialEntries: [opts.at ?? '/dashboard'] },
  );
}

/** Accessible names of the sidebar's buttons in document order, minus the unnamed ones and the collapse toggle. */
function buttonNames(): string[] {
  return screen
    .getAllByRole('button')
    .map((b) => b.getAttribute('aria-label') || b.textContent || '')
    .filter((n) => n !== '' && n !== 'Collapse sidebar' && n !== 'Expand sidebar');
}

describe('layout slots equivalence: Sidebar', () => {
  beforeEach(() => {
    navigateSpy.mockReset();
    setReviewCount(0);
  });

  it('expanded: the same entries in the same order', () => {
    renderSidebar({ collapsed: false });
    expect(buttonNames()).toEqual(EXPANDED.map(([label]) => label));
  });

  it.each(EXPANDED)('expanded: %s navigates to %s', (label, path) => {
    renderSidebar({ collapsed: false });
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^${label}$`) }));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith(path);
  });

  it('collapsed: the same top-level buttons in the same order', () => {
    renderSidebar({ collapsed: true });
    expect(buttonNames()).toEqual([...COLLAPSED_TOP]);
  });

  it('collapsed: the Library menu lists the same sub-entries and each navigates to its path', () => {
    renderSidebar({ collapsed: true });
    fireEvent.click(screen.getByRole('button', { name: 'Library' }));
    const menu = screen.getByRole('menu');
    expect(within(menu).getAllByRole('menuitem').map((m) => m.textContent)).toEqual(
      LIBRARY_MENU.map(([label]) => label),
    );
    for (const [label, path] of LIBRARY_MENU) {
      navigateSpy.mockReset();
      fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: label }));
      expect(navigateSpy).toHaveBeenCalledExactlyOnceWith(path);
      if (label !== LIBRARY_MENU[LIBRARY_MENU.length - 1][0]) {
        fireEvent.click(screen.getByRole('button', { name: 'Library' }));
      }
    }
  });

  it.each([false, true])('shows the pending-review count on Review only (collapsed=%s)', (collapsed) => {
    setReviewCount(7);
    renderSidebar({ collapsed });
    const review = screen.getByRole('button', { name: /^Review/ });
    expect(within(review).getByText('7')).toBeInTheDocument();
    for (const other of ['Dashboard', 'Activity', 'Diagnostics']) {
      const el = screen.getByRole('button', { name: new RegExp(`^${other}$`) });
      expect(within(el).queryByText('7')).not.toBeInTheDocument();
    }
  });

  it.each([false, true])('shows no badge text for a zero count (collapsed=%s)', (collapsed) => {
    renderSidebar({ collapsed });
    const review = screen.getByRole('button', { name: /^Review/ });
    expect(within(review).queryByText('0')).not.toBeInTheDocument();
  });

  it('follows the count live', () => {
    renderSidebar({ collapsed: false });
    const review = screen.getByRole('button', { name: /^Review/ });
    expect(within(review).queryByText('3')).not.toBeInTheDocument();
    // A store write outside React is how the poller publishes a count.
    act(() => setReviewCount(3));
    expect(within(screen.getByRole('button', { name: /^Review/ })).getByText('3')).toBeInTheDocument();
  });

  it.each([
    ['/library', true],
    ['/library/batman-year-one', true],
    ['/fingerprints', true],
    ['/series', true],
    ['/authors', true],
    ['/dashboard', false],
    ['/review', false],
  ])('collapsed: the Library icon at %s is selected=%s', (at, selected) => {
    renderSidebar({ collapsed: true, at });
    const library = screen.getByRole('button', { name: 'Library' });
    expect(library.classList.contains('Mui-selected')).toBe(selected);
  });

  it('expanded: the Library header is selected on /library only, not on a page under it', () => {
    renderSidebar({ collapsed: false, at: '/library/batman-year-one' });
    expect(screen.getByRole('button', { name: /^Library$/ })).not.toHaveClass('Mui-selected');
  });

  it('expanded: the Library group collapses and expands from its chevron', async () => {
    renderSidebar({ collapsed: false });
    expect(screen.getByRole('button', { name: 'Finished' })).toBeInTheDocument();
    const chevron = screen
      .getAllByRole('button')
      .find((b) => !b.getAttribute('aria-label') && !b.textContent);
    fireEvent.click(chevron!);
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Finished' })).not.toBeInTheDocument());
    fireEvent.click(chevron!);
    expect(await screen.findByRole('button', { name: 'Finished' })).toBeInTheDocument();
  });

  it.each([
    ['/dashboard', 'Dashboard'],
    ['/review', 'Review'],
    ['/settings', 'Settings'],
    ['/library', 'Library'],
  ])('expanded: %s highlights only %s among top-level entries', (at, label) => {
    renderSidebar({ collapsed: false, at });
    const selected = screen
      .getAllByRole('button')
      .filter((b) => b.classList.contains('Mui-selected'))
      .map((b) => b.textContent);
    // /library also lights the All Books sub-entry (its matchPath is /library).
    expect(selected).toContain(label);
    expect(selected.filter((n) => EXPANDED.some(([l]) => l === n) && n !== 'All Books')).toEqual([label]);
  });
});
