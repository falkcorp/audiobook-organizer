<!-- file: docs/agent-tasks/platform-core/seams.d/F9-03-prep-books-module.md -->
<!-- version: 0.1.0 -->
<!-- guid: a8b574f8-8f19-4323-9853-62ded27df908 -->
<!-- last-edited: 2026-11-22 -->

# F9-03 prep: booksModule (A3), seams for F9-03

`web/src/modules/books/module.tsx` defines `booksModule = defineModule(booksManifest, ...)`. Dark: nothing in `web/` imports it but `booksModule.test.tsx`, which mounts it on `createShell` in jsdom. F9-03 commit 2 passes it to `createShell`.

## What it fills

| Slot | Contents |
|---|---|
| routes | 14 pages under `/books/`: library, fingerprints (Library with the fingerprints preset), book `library/:id`, works, files, series, authors, author `authors/:id`, playlists, playlist, collections, collection, versions, review |
| nav | `booksNavItems` minus the entries the shell owns (Dashboard, Activity, Diagnostics, System, Users, Settings): the Library group (All Books with `matchPath: 'library'`, In Progress and Finished with `matchSearch`), Fingerprints, Series, Authors, File Browser, Works, Playlists, Collections, Review with `useBadge`; Library has `goKey: 'l'` |
| widgets | `library-home` (home), `ai-diagnostics` and `db-health` (diagnostics) |
| banners, topBarItems | `BooksBanners`, `LibrarySearch` |
| userPageExtensions | `UserMergeRowAction`, panels `UserMergesPanel` and `GuestPhoneKeysPanel` |
| taskKinds | `booksTaskKinds` |

## Gaps that remain (F9-03 fills them)

- `booksManifest` is hand-written: `internal/books/module.go` has no `UIDescriptor`. F9-03 generates the manifest and adds the same lists server-side, or the shell drops every item as undeclared. `permissions` is empty and `permissionsHints.view` is `[]`.
- `activityPanes`: `modules/books/activity/activityPanes.ts` (PR #5010) is not on this base; `// TODO(#5010)` in `module.tsx`.
- `settingsPanels` and `settingsSession`: the nine panels need a `SettingsSession` over `useBooksSettings` (dirty, save, discard) and a `Provider` that does not also render the floating save and the dialogs (the shell has `Footer` and `Dialogs` slots), and `useUnsavedChangesBlocker` has to go because the shell prompts itself.
- `setup`: `booksSetup` steps use `Component`, `BooksSetupProvider` takes `open` and `onComplete`; the shell wants `lazy` steps, a children-only `provider`, `useCanProceed` and `useFinish`.
- Links inside the moved pages, the widgets and `LibrarySearch` still go to unprefixed paths (`/library`); the move under `/books/` is F9-03.
- Route stubs: the test stubs five page modules, so it proves the `lazy` wiring (path and export name) but not that the real pages render under the shell's providers.
