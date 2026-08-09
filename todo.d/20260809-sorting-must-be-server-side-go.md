<!-- file: todo.d/20260809-sorting-must-be-server-side-go.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3a95c7e1-6b20-4d84-a7f9-1e60b52cf483 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **Replace library sorting with server-side Go sorting.** Owner decision 2026-08-09:
      *"I want the system to not suck and I want sorting replaced, and done by go."*
      Recorded in full with the code evidence in §0a of
      `docs/design/2026-08-09-search-backend-options.md`.

      ## Where sorting lives today — three places, one of them correct

      | where | what | verdict |
      |---|---|---|
      | `internal/audiobooks/service_filtering.go:130` `applySorting` | Go, server-side, over the full filtered set | **Correct.** Keep and extend |
      | `web/src/components/common/ConfigurableTable.tsx:201` | `[...rows].sort(...)` on the client | **Replace.** Sorts the *current page* |
      | `web/src/services/api.ts` `searchBooksPage` | sends no `sort_by` | **Fix.** Search drops the sort order entirely |

      **Why the client-side one is broken by design, not merely misplaced:** it sorts the
      rows already fetched. On a paginated library that is the 50 books you can see, not
      the library. "Sort by title descending" hands you the *wrong 50 books*, correctly
      ordered among themselves — which looks plausible, which is why it survives.

      ## Scope carefully — not every client sort is wrong

      There are **15** `.sort()` sites in `web/src`. Most are legitimate: a book's own file
      list by track number (`BookDetailFilesTab.tsx:250`), tag clouds by count
      (`TagCloud.tsx:76`), metadata candidates by score. Those sort complete, small,
      already-fetched sets. **The rule is: a sort over a paginated slice of the library is
      wrong; a sort over a complete set the client already holds is fine.**

      ## Two things that must land with it

      1. **Sort must be applied BEFORE pagination**, which is the same defect as filters
         being applied after pagination (§2.2 of the design doc). Moving the sort to Go
         without pushing it into the query would produce a correctly-sorted page of the
         wrong rows — a subtler bug than the one being fixed.
      2. **There is no sort control in the UI at all.** `SearchBarProps`
         (`web/src/components/audiobooks/SearchBar.tsx:124-131`) has no `onSortChange`, and
         `LibraryBookGrid.tsx:133` takes the handler as `_handleSortChange` —
         underscore-prefixed to mark it deliberately unused. The state, URL round-trip and
         API parameter all still work; only the affordance is missing. So "replace sorting"
         is partly **restore the control**, not only move the logic.
         `SearchBar.test.tsx:43` asserts the control is absent and now passes vacuously —
         that assertion has to be inverted, or it will defend the bug.

      **Acceptance:** choosing a sort reorders the whole library (verify by sorting
      descending and checking page 1 holds the true last items, not the reversed first
      page); the sort survives a search; `sort_by` appears on the request; no `.sort()`
      remains over a paginated library slice.
