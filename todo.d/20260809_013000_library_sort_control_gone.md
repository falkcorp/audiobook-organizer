- [ ] **The library "Sort by" control no longer exists — 4 e2e tests target a
      surface that is gone.** Found 2026-08-09 while repairing
      `library-browser.spec.ts`.

      `sorts books by title ascending` / `title descending` / `author` /
      `date added` all do:

          await page.getByRole('combobox', { name: 'Sort by' }).click();
          await page.getByRole('option', { name: 'Title' }).click();

      There is **no such control anywhere in the library UI**. Grepping the
      components turns up no `Sort by` label and no sort dropdown in
      `FilterPanel`, `LibraryToolbar` or `SearchBar`. Sorting now happens
      through the table view's column headers
      (`LibraryBookGrid`'s `handleColumnSortChange` → `ConfigurableTable` /
      `AudiobookList`), which the default grid view does not show at all.

      **This is a rewrite, not a selector tweak**, which is why it was left out
      of the mock-fix PR: the tests must switch to list/table view first and
      then drive column-header sorting, and the assertions about resulting
      order need to match however that view renders.

      The mock now honours `sort_by` / `sort_order` correctly (added
      2026-08-09), so once the tests drive the real control the backend half of
      this is already in place.

      **Check before rewriting:** confirm sorting is genuinely still reachable
      by a user in the default grid view. If it is not, that is a product
      question — "you can no longer sort the library without switching views"
      — and should be raised rather than encoded into a test.
