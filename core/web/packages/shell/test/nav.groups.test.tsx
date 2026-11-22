// file: core/web/packages/shell/test/nav.groups.test.tsx
// version: 0.1.0
// guid: 4e8a1c27-b5d3-4f90-a6e2-7c1d9b3f0a58
// last-edited: 2026-11-22

// F9-03 prep: NavContribution carries groups, matchPath, matchSearch and
// useBadge (07 §7.4.2 plus matchPath/matchSearch, see seams.d/F9-03-prep-nav.md),
// and the shell layout is MainLayoutFrame over SidebarFrame, so ABO's sidebar
// (the Library group and its isSubItemSelected rule) is expressible.

import { fireEvent, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ShellLayout } from '../src/app/ShellLayout';
import { grantsFor } from '../src/auth/hooks';
import { defineModule } from '../src/defineModule';
import { buildNav } from '../src/nav/navModel';
import { toSidebarItems } from '../src/nav/sidebarItems';
import { renderInShell } from '../src/testing';

const Page = () => Promise.resolve({ default: () => <div data-testid="page" /> });

const manifest = {
  id: 'gotham',
  version: '0.1.0',
  displayName: 'Gotham',
  pages: ['library', 'series', 'review'],
  nav: ['library', 'all', 'progress', 'series', 'review'],
  settingsPanels: [],
  widgets: [],
  banners: ['signal'],
  topBarItems: ['search'],
  userPage: [],
  activityPanes: [],
  setupSteps: [],
  permissions: ['gotham:read'],
} as const;

function gotham(useBadge?: () => number | undefined) {
  return defineModule(manifest, {
    routes: [
      { id: 'library', path: 'library', lazy: Page },
      { id: 'series', path: 'series/:id', lazy: Page },
      { id: 'review', path: 'review', lazy: Page },
    ],
    nav: [
      {
        id: 'library',
        label: 'Library',
        to: 'library',
        children: [
          { id: 'all', label: 'All Books', to: { page: 'library', search: 'reset=1' }, matchPath: 'library', matchSearch: '' },
          { id: 'progress', label: 'In Progress', to: { page: 'library', search: 'search=read_status:in_progress' }, matchSearch: 'read_status:in_progress' },
        ],
      },
      { id: 'series', label: 'Series', to: 'series', order: 3, matchPath: 'series/all' },
      { id: 'review', label: 'Review', to: 'review', order: 4, ...(useBadge ? { useBadge } : {}) },
    ],
    banners: [{ id: 'signal', lazy: () => Promise.resolve({ default: () => <div>Bat-Signal</div> }) }],
    topBarItems: [{ id: 'search', lazy: () => Promise.resolve({ default: () => <div>Search Gotham</div> }) }],
    settingsPanels: [],
    permissionsHints: { view: ['gotham:read'] },
  });
}

describe('nav model: groups and match rules', () => {
  it('resolves matchPath under /<module id>/ and keeps matchSearch and children', () => {
    const [library, series] = buildNav([gotham()], grantsFor(['gotham:read']));
    expect(library.children.map((c) => c.label)).toEqual(['All Books', 'In Progress']);
    expect(library.children[0]).toMatchObject({ href: '/gotham/library?reset=1', matchPath: '/gotham/library', matchSearch: '' });
    expect(library.children[1]).toMatchObject({ href: '/gotham/library?search=read_status:in_progress', matchSearch: 'read_status:in_progress' });
    expect(series.matchPath).toBe('/gotham/series/all');
  });

  it('maps to SidebarItems: group children, the badge hook, the query-free default matchPath, a fallback icon', () => {
    const useBadge = () => 2;
    const items = toSidebarItems(buildNav([gotham(useBadge)], grantsFor(['gotham:read'])));
    expect(items.map((i) => i.text)).toEqual(['Library', 'Series', 'Review']);
    expect(items[0].children?.map((c) => c.text)).toEqual(['All Books', 'In Progress']);
    expect(items[0].children?.[0]).toMatchObject({ path: '/gotham/library?reset=1', matchPath: '/gotham/library', matchSearch: '' });
    expect(items[0].children?.[1].matchPath).toBe('/gotham/library');
    expect(items[2].useBadge).toBe(useBadge);
    expect(items[2].icon).toBeTruthy();
  });

  it('rejects a matchPath that is not relative to the module', () => {
    expect(() =>
      defineModule(manifest, {
        routes: [{ id: 'library', path: 'library', lazy: Page }],
        nav: [{ id: 'library', label: 'Library', to: 'library', matchPath: '/library' }],
        settingsPanels: [],
        permissionsHints: { view: [] },
      }),
    ).toThrow(/matchPath/);
  });
});

describe('ShellLayout is MainLayoutFrame over SidebarFrame', () => {
  it('renders shell entries, the module group with its children, banners and top-bar slots', async () => {
    const mod = gotham();
    renderInShell(<ShellLayout />, { modules: [mod], permissions: ['gotham:read'] });
    expect(await screen.findByText('Bat-Signal')).toBeInTheDocument();
    expect(await screen.findByText('Search Gotham')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Library' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'All Books' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Activity' })).toBeInTheDocument();
    // The scaffold's Modules and Shell lists are gone: one sidebar, collapsible.
    expect(screen.getByRole('button', { name: 'Collapse sidebar' })).toBeInTheDocument();
  });

  it('keeps the Library rule: All Books on a plain visit, In Progress only with its search', async () => {
    const mod = gotham();
    renderInShell(<ShellLayout />, { modules: [mod], permissions: ['gotham:read'], route: '/gotham/library' });
    const all = await screen.findByRole('button', { name: 'All Books' });
    expect(all).toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'In Progress' })).not.toHaveClass('Mui-selected');
  });

  it('selects In Progress, not All Books, on ?search=read_status:in_progress', async () => {
    const mod = gotham();
    renderInShell(<ShellLayout />, { modules: [mod], permissions: ['gotham:read'], route: '/gotham/library?search=read_status%3Ain_progress&page=1' });
    expect(await screen.findByRole('button', { name: 'In Progress' })).toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'All Books' })).not.toHaveClass('Mui-selected');
  });

  it('selects a leaf by matchPath, and a shell entry on its sub-routes', async () => {
    const mod = gotham();
    renderInShell(<ShellLayout />, { modules: [mod], permissions: ['gotham:read'], route: '/gotham/series/all' });
    expect(await screen.findByRole('button', { name: 'Series' })).toHaveClass('Mui-selected');
    expect(screen.getByRole('button', { name: 'Home' })).not.toHaveClass('Mui-selected');
  });

  it('highlights Settings on /settings/<panel>', async () => {
    renderInShell(<ShellLayout />, { modules: [gotham()], permissions: ['gotham:read'], route: '/settings/gotham.cave' });
    expect(await screen.findByRole('button', { name: 'Settings' })).toHaveClass('Mui-selected');
  });

  it('shows the useBadge count on the entry, re-reading it each render', async () => {
    const useBadge = vi.fn(() => 7);
    const mod = gotham(useBadge);
    renderInShell(<ShellLayout />, { modules: [mod], permissions: ['gotham:read'] });
    const review = await screen.findByRole('button', { name: /Review/ });
    expect(within(review).getByText('7')).toBeInTheDocument();
    expect(useBadge).toHaveBeenCalled();
  });

  it('navigates when an entry is clicked, and hides Users and System without core:admin', async () => {
    const mod = gotham();
    const { router } = renderInShell(<ShellLayout />, { modules: [mod], permissions: ['gotham:read'] });
    expect(screen.queryByRole('button', { name: 'Users' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'System' })).not.toBeInTheDocument();
    fireEvent.click(await screen.findByRole('button', { name: 'In Progress' }));
    expect(router.state.location.pathname).toBe('/gotham/library');
    expect(router.state.location.search).toBe('?search=read_status:in_progress');
  });

  it('shows the account chip and signs out through the shared user menu', async () => {
    const logout = vi.fn(async () => {});
    const { router } = renderInShell(<ShellLayout />, { modules: [gotham()], permissions: ['gotham:read'], auth: { logout } });
    fireEvent.click(await screen.findByText('bruce'));
    // Core's client has no profile or password route yet, so those entries are absent.
    expect(screen.queryByText('Edit profile')).not.toBeInTheDocument();
    expect(screen.queryByText('Change password')).not.toBeInTheDocument();
    fireEvent.click(screen.getByText('Logout'));
    await vi.waitFor(() => expect(logout).toHaveBeenCalled());
    await vi.waitFor(() => expect(router.state.location.pathname).toBe('/login'));
  });
});
