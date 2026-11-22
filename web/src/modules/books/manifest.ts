// file: web/src/modules/books/manifest.ts
// version: 0.1.0
// guid: aa8ba615-e4e1-4563-969d-3a4ef003a9d4
// last-edited: 2026-11-22

// Books' client-side manifest: the IDs of everything the books module contributes to the
// shell (07 §7.4.1), in the shape of core/module.UIDescriptor. The server declares the same
// lists and the shell drops a client item the server does not declare (07 §7.5.2).
//
// Hand-written until F9-03 generates it from books' Go Manifest(): internal/books/module.go
// has no UIDescriptor yet, so booksModule.test.ts is the only thing keeping this list and the
// module's contributions in step (the shell's defineModule types every slot by these IDs).
//
// Not listed because the shell owns the page: Dashboard (home), Activity, System, Users,
// Settings and Diagnostics. Books fills them through widgets, user-page extensions and
// settings panels instead. activityPanes, settingsPanels and setupSteps are empty on purpose;
// see the TODOs in module.tsx.

export const booksManifest = {
  id: 'books',
  version: '1.0.0',
  displayName: 'Audiobooks',
  pages: [
    'library',
    'fingerprints',
    'book',
    'works',
    'files',
    'series',
    'authors',
    'author',
    'playlists',
    'playlist',
    'collections',
    'collection',
    'versions',
    'review',
  ],
  nav: [
    'library',
    'library-all',
    'library-in-progress',
    'library-finished',
    'fingerprints',
    'series',
    'authors',
    'files',
    'works',
    'playlists',
    'collections',
    'review',
  ],
  settingsPanels: [],
  widgets: ['library-home', 'ai-diagnostics', 'db-health'],
  banners: ['books-banners'],
  topBarItems: ['library-search'],
  userPage: ['user-merge', 'user-merges', 'guest-phone-keys'],
  activityPanes: [],
  setupSteps: [],
  permissions: [],
} as const;
