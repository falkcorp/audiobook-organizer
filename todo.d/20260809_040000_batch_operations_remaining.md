- [ ] **8 remaining failures in `batch-operations.spec.ts`** (down from 11), and
      a caution about how they were approached.

      **Fixed (3):** the "N selected" chip is rendered TWICE in the tree, so
      `getByText('1 selected')` was always a strict-mode violation. Assertions
      now use `.first()` — the behaviour under test is that the count shows, not
      how many places show it. *(If that duplication is itself unintended, it is
      a UI question worth asking separately.)*

      **Verified renames applied:** the toolbar button "Fetch Metadata" is now
      **"Fetch Selected"**, and "Deselect All" is now **"Deselect"** — read off
      the app's rendered accessible names, not guessed.

      **⚠️ Trap, hit and recorded:** the confirm button INSIDE the "Bulk Fetch
      Metadata" dialog is **still "Fetch Metadata"** (`LibraryDialogs.tsx`
      renders `Fetching…` / `Fetch Metadata`). A blanket find-and-replace of
      "Fetch Metadata" → "Fetch Selected" therefore breaks the dialog-scoped
      references. Only the toolbar button was renamed. The spec now carries a
      comment at that call site.

      **Still failing (8):** `deselects all books`, the five bulk-fetch tests,
      `batch updates metadata field`, and `disables batch operations when no
      books selected`. The count did NOT move across three separate attempts
      (chip fix, renames, dialog-scope correction), which means the remaining
      cause has not actually been found yet — the failures are almost certainly
      NOT more label drift.

      **Do this next, and do it first:** open the Playwright DOM snapshot for
      one bulk-fetch failure (`test-results/*/error-context.md`) and read what
      is actually on the page at the moment of failure. Every real cause found
      in this repair effort — the dedup page being redesigned behind a "Legacy
      View" toggle, metadata-provenance rendering the LOGIN screen, the book
      sub-resources returning the book object — was found that way, and every
      wrong guess came from reasoning about what *should* be there instead.
