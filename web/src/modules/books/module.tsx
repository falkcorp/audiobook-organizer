// file: web/src/modules/books/module.tsx
// version: 0.1.0
// guid: 30ff53b3-c280-42cd-8e7c-8dcddc43e243
// last-edited: 2026-11-22

// booksModule: everything the audiobook app contributes to the shell, bound to the manifest
// (07 §7.4.2, §7.11). DARK: nothing in the running app imports this file; main.tsx still
// mounts App.tsx. F9-03 commit 2 replaces that mount with
// createShell({ app, modules: [booksModule] }).
//
// What is deliberately not filled here, and why (each is a typed gap, not a silent one):
// - activityPanes: modules/books/activity/activityPanes.ts is PR #5010 and is not on this
//   base. Fill `activityPanes` and add the ids to booksManifest.activityPanes when it lands.
// - settingsPanels and settingsSession: the nine books panels (pages/booksSettings.ts) need the
//   shared form's Provider, a SettingsSession over useBooksSettings (dirty, save, discard)
//   and the unsaved-changes guard moved off useUnsavedChangesBlocker onto the shell's. That
//   is books code that does not exist yet, so the panels are left out rather than mounted
//   without their context.
// - setup: pages/booksSetup.ts steps take `Component` and BooksSetupProvider takes
//   `open` / `onComplete` props; the shell's SetupContribution wants `lazy` steps, a
//   children-only `provider` and a `useFinish` hook.
// - Page links inside the moved pages and widgets (and LibrarySearch) still navigate to
//   unprefixed paths such as /library; moving ABO under /books/ is F9-03.

import type { ComponentType } from 'react';
import BookmarksIcon from '@mui/icons-material/Bookmarks';
import CollectionsBookmarkIcon from '@mui/icons-material/CollectionsBookmark';
import FactCheckIcon from '@mui/icons-material/FactCheck';
import FolderOpenIcon from '@mui/icons-material/FolderOpen';
import LibraryBooksIcon from '@mui/icons-material/LibraryBooks';
import MenuBookIcon from '@mui/icons-material/MenuBook';
import PeopleIcon from '@mui/icons-material/People';
import WavesIcon from '@mui/icons-material/Waves';
import { defineModule } from '@mediacore/shell';
import type { Lazy } from '@mediacore/shell';
import { BooksBanners } from './layout/banners';
import { useReviewCount } from './layout/useReviewCount';
import { booksTaskKinds } from './layout/taskKinds';
import { booksManifest } from './manifest';

/** Marker for the dark proof: present in a build only if something imports this module. */
export const BOOKS_MODULE_MARKER = 'books-module-defined-for-the-shell';

/** A `lazy` entry for the component a module exports as `name`. */
function named<P = object>(load: () => Promise<Record<string, unknown>>, name: string) {
  return async (): Promise<{ default: ComponentType<P> }> => ({
    default: (await load())[name] as ComponentType<P>,
  });
}

const page = (load: () => Promise<Record<string, unknown>>, name: string): Lazy => named(load, name);

const libraryOf =
  (defaultPreset: 'standard' | 'fingerprints'): Lazy =>
  async () => {
    const { Library } = await import('../../pages/Library');
    return { default: () => <Library defaultPreset={defaultPreset} /> };
  };

export const booksModule = defineModule(booksManifest, {
  routes: [
    { id: 'library', path: 'library', lazy: libraryOf('standard'), title: 'Library' },
    { id: 'fingerprints', path: 'fingerprints', lazy: libraryOf('fingerprints'), title: 'Fingerprints' },
    { id: 'book', path: 'library/:id', lazy: page(() => import('../../pages/BookDetail'), 'BookDetail'), title: 'Book' },
    { id: 'works', path: 'works', lazy: page(() => import('../../pages/Works'), 'Works'), title: 'Works' },
    { id: 'files', path: 'files', lazy: page(() => import('../../pages/FileBrowser'), 'FileBrowser'), title: 'File Browser' },
    { id: 'series', path: 'series', lazy: page(() => import('../../pages/Series'), 'Series'), title: 'Series' },
    { id: 'authors', path: 'authors', lazy: page(() => import('../../pages/Authors'), 'Authors'), title: 'Authors' },
    { id: 'author', path: 'authors/:id', lazy: () => import('../../pages/AuthorDetail'), title: 'Author' },
    { id: 'playlists', path: 'playlists', lazy: () => import('../../pages/Playlists'), title: 'Playlists' },
    { id: 'playlist', path: 'playlists/:id', lazy: () => import('../../pages/PlaylistDetail'), title: 'Playlist' },
    { id: 'collections', path: 'collections', lazy: () => import('../../pages/Collections'), title: 'Collections' },
    { id: 'collection', path: 'collections/:id', lazy: () => import('../../pages/CollectionDetail'), title: 'Collection' },
    { id: 'versions', path: 'versions', lazy: () => import('../../pages/TrashedVersions'), title: 'Trashed Versions' },
    {
      id: 'review',
      path: 'review',
      lazy: page(() => import('../../components/review/ReviewWorkspace'), 'ReviewWorkspace'),
      title: 'Review',
    },
  ],

  // booksNavItems minus the entries the shell owns (Dashboard, Activity, Diagnostics, System,
  // Users, Settings), in the same order, with the same selection rules. modules/books/
  // booksModule.test.tsx checks the two lists against each other.
  nav: [
    {
      id: 'library',
      label: 'Library',
      icon: LibraryBooksIcon,
      to: 'library',
      order: 10,
      goKey: 'l',
      children: [
        {
          id: 'library-all',
          label: 'All Books',
          icon: LibraryBooksIcon,
          // ?reset=1 makes Library.tsx do a true full reset; matchPath keeps the highlight on a plain visit.
          to: { page: 'library', search: 'reset=1' },
          matchPath: 'library',
          matchSearch: '',
        },
        {
          id: 'library-in-progress',
          label: 'In Progress',
          icon: MenuBookIcon,
          to: { page: 'library', search: 'search=read_status:in_progress' },
          matchSearch: 'read_status:in_progress',
        },
        {
          id: 'library-finished',
          label: 'Finished',
          icon: LibraryBooksIcon,
          to: { page: 'library', search: 'search=read_status:finished' },
          matchSearch: 'read_status:finished',
        },
        { id: 'fingerprints', label: 'Fingerprints', icon: WavesIcon, to: 'fingerprints' },
        { id: 'series', label: 'Series', icon: CollectionsBookmarkIcon, to: 'series' },
        { id: 'authors', label: 'Authors', icon: PeopleIcon, to: 'authors' },
      ],
    },
    { id: 'files', label: 'File Browser', icon: FolderOpenIcon, to: 'files', order: 20 },
    { id: 'works', label: 'Works', icon: MenuBookIcon, to: 'works', order: 30 },
    { id: 'playlists', label: 'Playlists', icon: MenuBookIcon, to: 'playlists', order: 40 },
    { id: 'collections', label: 'Collections', icon: BookmarksIcon, to: 'collections', order: 50 },
    { id: 'review', label: 'Review', icon: FactCheckIcon, to: 'review', order: 60, useBadge: useReviewCount },
  ],

  // TODO(settings): booksSettingsPanels plus a settingsSession; see the header.
  settingsPanels: [],
  permissionsHints: { view: [] },

  widgets: [
    {
      id: 'library-home',
      placement: 'home',
      lazy: page(() => import('./pages/LibraryHomeWidget'), 'LibraryHomeWidget'),
      order: 10,
    },
    {
      id: 'ai-diagnostics',
      placement: 'diagnostics',
      lazy: page(() => import('./pages/AIDiagnosticsWidget'), 'AIDiagnosticsWidget'),
      order: 10,
    },
    {
      id: 'db-health',
      placement: 'diagnostics',
      lazy: page(() => import('./pages/DBHealthWidget'), 'DBHealthWidget'),
      order: 20,
    },
  ],
  banners: [{ id: 'books-banners', lazy: async () => ({ default: BooksBanners }) }],
  topBarItems: [
    { id: 'library-search', lazy: page(() => import('./layout/LibrarySearch'), 'LibrarySearch') },
  ],
  userPageExtensions: {
    rowActions: [
      {
        id: 'user-merge',
        label: 'Merge account',
        lazy: named(() => import('./pages/UserMergeRowAction'), 'UserMergeRowAction'),
      },
    ],
    panels: [
      {
        id: 'user-merges',
        label: 'Account merges',
        lazy: named(() => import('./pages/UserMerge'), 'UserMergesPanel'),
        order: 10,
      },
      {
        id: 'guest-phone-keys',
        label: 'Guest phone keys',
        lazy: named(() => import('./pages/GuestPhoneKeysPanel'), 'GuestPhoneKeysPanel'),
        order: 20,
      },
    ],
  },
  // TODO(#5010): activityPanes from ./activity/activityPanes.ts.
  taskKinds: booksTaskKinds,
});
