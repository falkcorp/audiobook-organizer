### (Deferred) Optional global end-to-end % bar for library scans

The 2026-09-09 scan-progress fix made every phase report a *truthful* signal:
indeterminate phases show an animated bar + count, and the per-book "Processed"
phase shows a book-unit bar whose denominator grows folder-by-folder as books are
discovered. What it deliberately does **not** provide is a single library-wide
percentage that is known before the first folder finishes.

Providing one truthfully would require a **discovery-only pre-walk** (readdir, no
tag reads) across every configured root to count folders up front, then driving
the main bar off that fixed folder denominator. This was deferred because it adds
a second full directory traversal of a ~54 TB network volume every scan for a
marginal UX gain over the per-folder bar now shipped.

A second, related gap the same pre-walk would close: on a **multi-folder
resume**, `discoveredBooks` only accumulates folders from `ResumeFolderIdx`
onward (earlier folders are skipped via `continue`), so the denominator is
scoped to the resumed run's folders rather than the whole library. In practice
this does not bite the current prod scan, which walks the organized RootDir as a
single `ScanDirectoryParallel` folder and resumes *within* that folder
(`folderIdx == ResumeFolderIdx`), so the full `len(books)` is counted and the
bar continues unbroken (e.g. 29021/30548). It only matters if a large folder
completes and a later small folder dies. The clean fix is to persist the
previous run's discovered-books total in the checkpoint (alongside folderIdx /
itemOffset) and seed `discoveredBooks` with it on resume — no re-walk needed.

Notes for whoever picks this up:
- A *book* pre-count is not an option: `groupFilesIntoBooks` reads tags to group
  files into books, so counting books costs the same as the scan's slow phase.
- A *file* pre-count is the whole-tree walk that was just removed for being a
  second traversal producing a file-unit number against a book numerator.
- The stale branch `fix/scan-progress-monotonic` (commit 34adc1b57, unmerged)
  took exactly this pre-walk approach AND had a latent bug: its `bookProgressUnits`
  used `len(SegmentFiles)`, which is empty for single-album-directory books, so it
  would undercount and never reach 100% for those. Delete that branch; do not
  revive it as-is.
