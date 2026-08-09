<!-- file: todo.d/20260809-library-sort-control-missing.md -->
<!-- version: 1.0.0 -->
<!-- guid: 91c4e7b3-2d68-4a15-8f09-5e63b7a2d40c -->
<!-- last-edited: 2026-08-09 -->

- [ ] **You cannot sort the library from the UI.** The "Sort by" and "Order"
      comboboxes are gone. `SearchBarProps`
      (`web/src/components/audiobooks/SearchBar.tsx:124-131`) has no `onSortChange`
      prop at all, and `web/src/components/library/LibraryBookGrid.tsx:133` receives
      the handler as `_handleSortChange` — underscore-prefixed to mark it deliberately
      unused. Everything downstream still works: `Library.tsx` holds `sortBy`/`sortOrder`,
      writes them to the URL as `sort`/`order`, restores them on load, and passes them
      to the API. So sorting is fully functional and completely unreachable — the only
      way to change it is to hand-edit the URL.
      `SearchBar.test.tsx:43` asserts "does not render sort controls when `onSortChange`
      is absent", which now passes vacuously since the prop cannot be supplied.
      Four `library-browser.spec.ts` tests were repointed at the URL on 2026-08-09 so
      the sort *behaviour* stays covered while the control is missing.
      **Was this intentional?** If so the dead state and the vacuous unit test should
      be cleaned up; if not, the control needs restoring.
