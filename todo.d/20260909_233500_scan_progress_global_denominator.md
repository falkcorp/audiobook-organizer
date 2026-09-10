### Scan progress: give the per-book bar a fixed library-wide denominator

The 2026-09-09 scan-progress fix (PR #3173) made the two discovery phases report a
*truthful* indeterminate signal (animated bar + count, `progress_total=0`) and gave
the per-book "Processed" phase a **book-unit** denominator (`discoveredBooks`) that
always reaches 100%. Those discovery-phase fixes are verified live in prod and are
correct. This task is the follow-up the per-book phase still needs.

**The gap, confirmed in prod 2026-09-10 01:05 EDT.** The live scan is **15,744
folders** (`Scanning folder 20/15744`, `21/15744`), and most hold exactly one book
(`scan started: 1 books to process` → `Processed: 18621/18621`). Because
`scanFolder` does `discoveredBooks.Add(len(books))` immediately before processing
that folder, the per-book **total climbs in lockstep with `current`** on this shape
(`18620/18620` → `18621/18621` → …): the bar sits pinned at ~100% while both numbers
race upward. That is the user's original "total counts up" symptom, relocated from
the discovery phases into the per-book phase. It is unit-correct (books/books,
always tops out) but no more informative than a spinner for a many-small-folder
library.

Note the old (pre-#3173) code showed a *fixed* `29021/30548` here — but 30548 was
`len(scanCache)`, a **file** count that only coincidentally ≈ the book count on this
single-file-per-book library; it under-runs and tops the bar out early on multi-file
books (the ~11% case #3173 set out to fix). So #3173 did not break correctness — it
traded a coincidentally-fixed denominator for an always-correct climbing one. This
task is to get *both*: fixed and correct.

**The cheap fix — no pre-walk needed (this corrects the earlier premise here).**
`len(foldersToScan)` is known at loop entry; it is already the bounded denominator
of the `Scanning folders: 17028/17028` phase that #3173 correctly left alone. Drive
the per-book bar off **folder** progress (`folderIdx / len(foldersToScan)`) rather
than book progress and the denominator is fixed from the first folder, at zero extra
cost — no second directory traversal, which is the whole reason the book/file
pre-walk (below) was deferred. An earlier version of this note claimed a fixed
library-wide denominator *requires* a discovery-only pre-walk; that is true only for
a fixed *book*-unit denominator, not for the folder-unit one.

**The tradeoff to design around, not ignore.** Naive folder-unit progress is coarse
when folders hold uneven book counts, and it *regresses the single-big-folder shape*:
one folder of 30k books would sit at 0% until the whole folder finishes. The robust
form is a **scaled folder denominator plus a within-folder fraction** — advance the
bar by `(folderIdx + booksProcessedInThisFolder/len(books)) / len(foldersToScan)`, so
it moves smoothly whether folders hold 1 book or 30k. Keep the `Processed: N/M books`
count in the **message text** (it is useful); it is only the **bar** that needs the
fixed denominator.

**Separate, still-open: the multi-folder resume scoping.** `discoveredBooks` only
accumulates folders from `ResumeFolderIdx` onward (earlier folders are `continue`d),
so on a resume the book-unit denominator is scoped to the resumed run's folders, not
the whole library. The folder-unit bar above mostly sidesteps this (folder index is
absolute), but if the book-unit count is kept anywhere, persist the previous run's
discovered total in the checkpoint and seed `discoveredBooks` with it on resume.

Notes for whoever picks this up:
- A *book* pre-count is not an option: `groupFilesIntoBooks` reads tags to group
  files into books, so counting books up front costs the same as the scan's slow
  phase. The folder count does not — `foldersToScan` is already built.
- A *file* pre-count is the whole-tree walk #3173 removed (a second traversal
  producing a file-unit number against a book numerator).
- The stale branch `fix/scan-progress-monotonic` (commit 34adc1b57, unmerged)
  took the pre-walk approach AND had a latent bug: its `bookProgressUnits` used
  `len(SegmentFiles)`, which is empty for single-album-directory books, so it
  undercounts and never reaches 100% for those. Delete that branch; do not revive
  it as-is. The folder-unit approach above is simpler and needs none of it.
