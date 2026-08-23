// file: web/src/components/layout/Sidebar.test.tsx
// version: 1.0.0
// guid: edade879-0563-463d-bc62-bd740239b04b
// last-edited: 2026-08-21

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { Sidebar } from './Sidebar';
import { useReviewStore } from '../../stores/useReviewStore';

// Spy on useNavigate so the click-navigates assertions are direct call
// checks rather than route introspection, matching the pattern already used
// by ReviewBanner.test.tsx for the same store.
//
// Every render below passes `open={false}`. Sidebar renders TWO drawers: a
// mobile temporary one (always expanded, `open` prop controls visibility) and
// a desktop permanent one (always visible, `collapsed` prop controls icon-only
// mode). jsdom does not apply the `sx={{ display: { xs: ..., sm: ... } }}`
// breakpoint hiding, so with `open={true}` both drawers are simultaneously
// queryable and there are two elements named "Library" — one expanded (mobile,
// text label) and one collapsed (desktop, Tooltip aria-label). `open={false}`
// closes the (irrelevant, here) mobile modal so `getByRole` unambiguously
// finds the collapsed desktop button under test — do not "fix" this back to
// `open` or the queries below become ambiguous again.
const navigateSpy = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigateSpy };
});

describe('Sidebar (collapsed mode — Library sub-nav)', () => {
  beforeEach(() => {
    navigateSpy.mockReset();
    useReviewStore.setState({
      count: 0,
      byKind: {},
      items: [],
      itemsLoading: false,
      _pollTimer: null,
    });
  });

  it('opens a Library menu on click, listing every sub-item, and navigates on selection', () => {
    renderWithProviders(
      <Sidebar open={false} onClose={vi.fn()} drawerWidth={240} collapsed />,
      { initialEntries: ['/dashboard'] },
    );

    // Collapsed mode renders no label text, so the icon-only button must
    // still expose an accessible name — Tooltip('Library') already provides
    // one via aria-label, the same mechanism every other collapsed top-level
    // item in this sidebar relies on.
    fireEvent.click(screen.getByRole('button', { name: 'Library' }));

    expect(screen.getByRole('menuitem', { name: 'In Progress' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Finished' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'All Books' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('menuitem', { name: 'In Progress' }));

    expect(navigateSpy).toHaveBeenCalledWith('/library?search=read_status:in_progress');
    // Selecting an item closes the menu.
    expect(screen.queryByRole('menuitem', { name: 'Finished' })).not.toBeInTheDocument();
  });

  it('navigates Finished to its own filtered path', () => {
    renderWithProviders(
      <Sidebar open={false} onClose={vi.fn()} drawerWidth={240} collapsed />,
      { initialEntries: ['/dashboard'] },
    );

    fireEvent.click(screen.getByRole('button', { name: 'Library' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Finished' }));

    expect(navigateSpy).toHaveBeenCalledWith('/library?search=read_status:finished');
  });

  it('highlights the active sub-item in the collapsed menu the same way expanded mode does', () => {
    // Same URL, same isSubItemSelected() matcher (#2193) driving both modes —
    // the decoded search-param comparison, never a raw pathname/search string.
    renderWithProviders(
      <Sidebar open={false} onClose={vi.fn()} drawerWidth={240} collapsed />,
      { initialEntries: ['/library?search=read_status%3Ain_progress&page=1'] },
    );

    fireEvent.click(screen.getByRole('button', { name: 'Library' }));

    expect(screen.getByRole('menuitem', { name: 'In Progress' })).toHaveClass('Mui-selected');
    expect(screen.getByRole('menuitem', { name: 'Finished' })).not.toHaveClass('Mui-selected');
    expect(screen.getByRole('menuitem', { name: 'All Books' })).not.toHaveClass('Mui-selected');
  });

  it('keeps the Library icon selected while on a Library-family route even though no single sub-item can show', () => {
    // A user already on a Library sub-route who collapses the sidebar must
    // still see the broader "on some Library-family page" indicator (L177-182
    // in the expanded reading) even though the menu can no longer show WHICH
    // sub-item is active without being opened.
    renderWithProviders(
      <Sidebar open={false} onClose={vi.fn()} drawerWidth={240} collapsed />,
      { initialEntries: ['/fingerprints'] },
    );

    expect(screen.getByRole('button', { name: 'Library' })).toHaveClass('Mui-selected');
  });
});
