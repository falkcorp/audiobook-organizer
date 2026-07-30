<!-- file: docs/agent-tasks/abs-sync/TASK-07-scanner-chapter-hook.md -->
<!-- version: 1.0.0 -->
<!-- guid: 16ae5477-eb1f-4029-af23-3d1ff2b73a91 -->
<!-- last-edited: 2026-07-30 -->

# TASK-07 — Extract + persist chapters at scan time (abs-sync Phase 4)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · go-backend subagent, ideally
split as *(1) a code-exploration pass to re-verify the two save-call insertion points in
`internal/scanner/scanner.go`, since that file is ~2,300 lines with several book-save branches, then
(2) implement in `process_file.go`* · **Why:** the only genuinely tricky part is finding where a book's
BookFiles are guaranteed persisted before chapter extraction can run — the extraction itself just calls
already-merged `internal/audioutil` primitives · **Depends on:** TASK-06 (needs
`database.Chapter`/`GetChaptersForBook`/`SaveChaptersForBook` to exist first — do not start this until
TASK-06 is merged)

**Gate:** EXECUTE AUTONOMOUSLY (worktree → implement → PR → CI → merge). Nothing here is destructive.

**File-ownership:** `internal/scanner/process_file.go` (+ a new `internal/scanner/chapter_persistence_test.go`)
is the ONLY file this task adds real logic to. It also makes a **small, additive edit to
`internal/scanner/scanner.go`**: two one-line call-site insertions (see Step 4) — no other abs-sync
task in the wave table owns `scanner.go`, but re-verify with
`grep -rln 'scanner\.go' docs/agent-tasks/abs-sync/TASK-*.md` before merging in case a sibling brief has
since claimed it. This task does **not** touch `internal/audioutil/**` (already merged, read-only
dependency) and does **not** touch `internal/database/**` (TASK-06's territory).

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-scanner-chapter-hook" -b agent/abs-sync-scanner-chapter-hook origin/main
cd "$REPO/.worktrees/abs-sync-scanner-chapter-hook"
git rebase origin/main
```

**Before anything else, confirm TASK-06 is merged into `origin/main`:**
```bash
grep -n "func (p \*PebbleStore) GetChaptersForBook" internal/database/pebble_store_chapters.go
```
Expected: 1 hit. If this file doesn't exist yet, STOP — do not reimplement TASK-06's work here; wait
for it to merge and re-run `git rebase origin/main`.

## Goal

Wire the already-merged `internal/audioutil` chapter primitives (`ProbeChapters`,
`CumulativeOffsets`/`SynthesizeChapters`) and the newly-merged `database.Chapter` persistence
(TASK-06) into the scan pipeline, so every book gets a chapter list without a separate backfill job.
Two cases, both driven by real ABS ground truth
(`docs/specs/2026-07-29-abs-sync-api-design.md` §1.8.5, §5b):

1. **Single-file book** (e.g. one `.m4b`): trust its embedded chapters as-is.
2. **Multi-file book** (e.g. 6 `.mp3` tracks): **one synthesized chapter per file**, titled from each
   file's own embedded title tag (fallback: filename) — never from that file's own embedded
   sub-chapters, even if present.

Also settle the **§5b duration-skew finding**: three legitimate durations for the same real book
disagree by ~52ms (independently re-measured against the committed fixture during this brief's
authoring, matching the spec exactly):

| Source | Value (s) | Command used to verify |
|---|---|---|
| m4b container duration | 9975.480544 | `ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 odyssey_complete.m4b` |
| m4b **last embedded chapter's** end | 9975.428000 | `ffprobe -v error -show_chapters -print_format json odyssey_complete.m4b` (chapter index 5, `end_time`) |
| Sum of the 6 mp3 track durations | 9975.431111 | `ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1` on each `odyssey_0{1..6}_homer_butler_64kb.mp3`, summed |

**This task's mandate:** for a multi-file book, build chapters from **re-probed, unrounded per-track
durations** (`audioutil.ProbeDurationSeconds`, a `float64`) — **never** from the already-persisted
`BookFile.Duration` (which is an `int`, i.e. already rounded, and may additionally be a
filesize/bitrate *estimate* — see `BookFile.DurationEstimated` semantics in
`internal/mediainfo/mediainfo.go`). This keeps the synthesized timeline's total (the last chapter's
`EndSec`) equal to the **sum of track durations** — the §5b-recommended authoritative value, since it
matches real ABS `startOffset` values exactly.

**LABELED SCOPE DECISION — read this before objecting that §5b said "plus a ≥2s finished-detection
tolerance":** this task does **not** implement that tolerance. There is no finished-detection code
anywhere in this task's file-ownership (`process_file.go`) to hang it on — the mechanism that compares
`currentTime` against `duration` lives in the progress adapter, which doesn't exist yet
(`docs/specs/2026-07-29-abs-sync-api-design.md` §5, "Opus-owned; implemented in Phase 6" over
`UserBookState`/`UserPosition`). Building it here would mean inventing a progress-comparison code path
outside this task's scope and file-ownership grant. This task's actual, narrower job re: §5b is:
make sure the **precise** (unrounded) duration is the one that ends up in the persisted chapter data,
so Phase 6 has an accurate number to build the tolerance on top of instead of re-deriving one (wrongly)
from a rounded int or the container's own duration. **State this deferral explicitly in the PR body** so
a human reviewer signs off on it rather than discovering it was quietly dropped.

**Heads-up for whoever builds Phase 6 (leave this as a comment in the code, not just in this brief):**
after this task ships, a multi-file book has **two persisted durations that disagree by design** —
`Book.Duration` (an `*int`, container/estimate-derived, set by the pre-existing scan path) and
`chapters[len-1].EndSec` (a `float64`, sum-of-tracks, set by this task). That is exactly the footgun
§5b warns about, now baked into two different fields instead of one. Phase 6 must read the chapter-
derived value for finished-detection math, not `Book.Duration` — a source-code comment saying so is a
weaker guard than a test, so Phase 6's own task brief should assert this with a real regression test
once that code exists (out of scope to write here, since the code it would test doesn't exist yet).

## Background (verify before editing — this is the part most likely to have drifted)

- **The insertion anchor named in the spec is confirmed correct.** `docs/specs/2026-07-29-abs-sync-api-design.md`
  §1 says: "Insertion hook for extraction: `internal/scanner/process_file.go:41` (single place each
  file is opened once)." Verify:
  ```bash
  sed -n '41p' internal/scanner/process_file.go
  ```
  Expected: `func ProcessFile(filePath string) (*metadata.Metadata, *mediainfo.MediaInfo, string, error) {`
  **Important correction to internalize before writing code:** `ProcessFile` itself takes neither a
  `context.Context` nor an ffprobe path, and it is called from inside `metadata.Metadata`/tag-reading
  logic that has nothing to do with book IDs or the database. Do **not** try to shoehorn chapter
  extraction into `ProcessFile`'s body or change its signature (many callers, out of scope, and it runs
  once per raw file before any Book/BookFile row exists). Instead, add new, separate functions to this
  *same file* (satisfying the file-ownership grant) that are called from `scanner.go` **after** a
  book's row (and, for multi-file books, its `BookFile` rows) are persisted — see the two call sites in
  Step 4. This is why the task is titled "scanner-chapter-hook", not "process-file chapter arg".
- **`ffprobe` already runs once per file today**, inside `ProcessFile`'s call to
  `mediainfo.BuildFromTag` → `realDurationSec` → `audioutil.ProbeDurationSeconds`. Verify:
  ```bash
  grep -n "func realDurationSec" -A 8 internal/mediainfo/mediainfo.go
  grep -n "realDurationSec(filePath)" internal/mediainfo/mediainfo.go
  ```
  Expected: the function creates its own `context.WithTimeout(context.Background(), ffprobeDurationTimeout)`
  (20s) and calls `audioutil.ProbeDurationSeconds(ctx, "", filePath)`; two call sites (~120, ~293) for
  the MP3/M4A/M4B/FLAC/OGG cases. **This means adding one more ffprobe call per file for chapters is
  additive cost on top of an existing subprocess call, not a first one** — cite this when justifying
  the perf impact in the PR body, but do not use it as license to add *unbounded* extra calls (see the
  concurrency note below).
- **`scanner.Book` (the in-memory scan-time struct) has NO `ID` field.** This is an easy wrong
  assumption to make and will not compile. Verify:
  ```bash
  grep -n "^type Book struct" -A 20 internal/scanner/scanner.go | grep -c "ID "
  ```
  Expected: `0`. The scan pipeline instead always re-looks-up the persisted `database.Book` by
  **file path** after saving — `createBookFilesForBook` does exactly this
  (`getStore().GetBookByFilePath(bookFilePath)`, confirm at
  `grep -n 'GetBookByFilePath(bookFilePath)' internal/scanner/scanner.go`, expected ~1 hit
  ~scanner.go:1439). Your new function must do the same: take a `bookFilePath string`, not a bookID.
- **`database.Book` (the persisted DB row, a different type from `scanner.Book`) DOES have `ID` and
  `FilePath`.** Verify:
  ```bash
  grep -n "^type Book struct" -A 8 internal/database/store.go
  ```
  Expected: `ID string \`json:"id"\`` and `FilePath string \`json:"file_path"\`` in the first few
  fields.
- **`getStore()` returns the `database.Store` *interface*, which does NOT include TASK-06's new
  chapter methods** (they live only on the concrete `*PebbleStore`, per that task's deliberate scope —
  `Store`'s definition in `internal/database/store.go` is off-limits to both tasks). You must type-assert:
  ```bash
  grep -n "^func getStore() database.Store" internal/scanner/scanner.go
  ```
  Expected: 1 hit, ~scanner.go:173. Your new code calls `store.(*database.PebbleStore)`.
- **Verified: the concrete-type assertion is correct against production's ACTUAL wiring today — trace
  the whole chain before assuming it might not be:**
  ```bash
  grep -n "initializeStore        = database.InitializeStore" cmd/root.go     # ~root.go:41
  grep -n "s, err = NewPebbleStore(path)" internal/database/store.go          # ~store.go:1139, inside InitializeStore
  grep -n "scanner.SetStore(resolvedStore)" internal/server/server.go         # ~server.go:380, inside NewServer
  grep -rn "scanner.SetStore(" internal/server                                # confirm this is the ONLY call site
  grep -n "wrapped := &indexedStore{Store: inner, server: s}" internal/server/server_lifecycle.go
  ```
  The chain: `cmd/root.go` calls `database.InitializeStore`, which constructs a concrete
  `*database.PebbleStore` and returns it as the `Store` interface — `scanner.SetStore` (called once,
  inside `server.NewServer`, from `server.go:380`) receives that same still-unwrapped value. **The
  `*indexedStore` decorator (`internal/server/indexed_store.go`) is installed later**, inside
  `Start()` (`server_lifecycle.go`), and — confirm this with the second grep above — **nothing ever
  calls `scanner.SetStore` again with the wrapped value.** So `getStore()` inside the scanner package
  always returns the raw `*database.PebbleStore` in production today, and the assertion succeeds.
  **This is a real but latent trap, not a hypothetical one:** if a future change ever fixes that gap
  (the scanner package not tracking the `indexedStore` wrap is itself a known pre-existing oversight,
  referenced in `scanner.go`'s own comment about `SERVER-GLOBAL-STORE-AUDIT phase 7`) by adding a second
  `scanner.SetStore(wrapped)` call, this assertion would start failing — `indexedStore` embeds the
  `database.Store` *interface* (not the concrete `*PebbleStore`), so Go method promotion would **not**
  give it your new chapter methods either, and the type assertion would fail cleanly rather than
  silently succeeding on the wrong type. Because that failure mode is real (just not triggered today),
  do **not** make the failed-assertion path a silent no-op forever — see the next bullet.
- **On assertion failure, warn once — do not swallow it silently.** This file already has the pattern
  to copy: `scanner.go` defines package-level `atomic.Int64` counters (`dupLookupErrCount`,
  `scanCacheUpdateErrCount`, etc.) plus a shared `warnSampled(c *atomic.Int64, log logger.Logger,
  format string, args ...interface{})` helper that logs on the 1st occurrence and every 1000th after
  (verify: `grep -n "func warnSampled" internal/scanner/scanner.go`). Add one more such counter (e.g.
  `chapterStoreAssertErrCount`) and call `warnSampled` from `PersistChaptersForBook` when the
  `*database.PebbleStore` assertion fails, instead of returning `nil` with no trace at all. A silent,
  permanent no-op on a wiring mismatch is exactly the failure class this codebase's own H5 audit
  (referenced throughout `scanner.go`) was created to eliminate — don't reintroduce it here. (Missing
  chapters when the store genuinely has no chapters yet, or when extraction itself fails, are still
  fine to treat as quiet non-fatal no-ops — this warning is specifically for "the store isn't the type
  I expected," a wiring bug, not a data condition.)
- **`GetBookFiles`/`GetBookByFilePath` themselves ARE on the `Store` interface** (declared in
  `iface_misc.go`/`iface_book.go`, not `store.go` directly — `Store` embeds many small per-domain
  interfaces). You can call these two through the plain `store` interface value; only the
  chapter-specific calls need the concrete-type assertion. Verify:
  ```bash
  grep -n "GetBookFiles(bookID string) (\[\]BookFile, error)" internal/database/iface_misc.go
  grep -n "GetBookByFilePath(path string) (\*Book, error)" internal/database/iface_book.go
  ```
  Expected: 1 hit each.
- **`BookFile.Title` is already populated from the file's own embedded title tag** at BookFile-creation
  time — this is exactly the "track's embedded title tag" `audioutil.TrackInfo.Title` wants. Verify:
  ```bash
  grep -n "bf.Title = meta.Title" internal/scanner/scanner.go
  ```
  Expected: 1 hit, inside `createBookFilesForBook` (~scanner.go:1502). Independently confirmed on the
  real fixture: `ffprobe -show_entries format_tags=title odyssey_01_homer_butler_64kb.mp3` reports
  `TAG:title=The Odyssey: Book 01` — this is what ends up in `BookFile.Title`, and is what
  `audioutil.SynthesizeChapters` should title each chapter from (never the filename, when the tag is
  present).
- **Only two call sites need the new hook**, both **after** a book's row and (for multi-file books) its
  `BookFile` rows are guaranteed to already be in the store. Re-verify both (content match, not line
  number, since `scanner.go` changes often):
  ```bash
  grep -n "createBookFilesForBook(dirPath, nil, scanLog)" internal/scanner/scanner.go
  # Expected: 1 hit, ~scanner.go:846 — the directory-based (album-tagged folder) book branch,
  # which unconditionally creates BookFiles for every file found in the directory.
  grep -n "createBookFilesForBook(books\[idx\].FilePath, books\[idx\].SegmentFiles, scanLog, books\[idx\].SegmentHashes)" internal/scanner/scanner.go
  # Expected: 1 hit, ~scanner.go:1023 — the shared save point for BOTH a genuinely single-file book
  # (SegmentFiles empty, this call is skipped by its own `if len(...) > 1` guard, so no BookFile rows
  # exist for it — Book.FilePath IS the one audio file) AND a multi-file "generic part filename"/
  # album-in-mixed-directory book (SegmentFiles > 1, BookFile rows get created here).
  ```
  A third `saveBook` call exists (~scanner.go:1146, inside the AI-batch-reprocessing loop) — this is a
  **metadata-only re-save** after AI parsing fills in title/author on an already-fully-processed book;
  it does not create new files and must NOT get a chapter-hook call (chapters were already persisted at
  the first save, and the idempotency check in Step 3 would just skip it anyway, but don't add the call
  there — it's the wrong place and adds a needless `GetBookByFilePath` lookup on every AI re-save).
- **`createBookFilesForBook` already has an idempotency guard for rescans** — `if len(existing) > 0 {
  return }` (verify: `grep -n 'BookFiles already created' internal/scanner/scanner.go`, expected ~1 hit
  ~scanner.go:1445). Your new chapter hook needs an **equivalent, independent** guard (check
  `GetChaptersForBook` is already non-empty) so a rescan of an unchanged book doesn't re-run ffprobe
  every time — see Step 3.
- **Concurrency: the existing scan is already parallel and bounded.** `ScanDirectoryParallel` and
  `ProcessBooksParallel` both take a `workers int` and use a `semaphore := make(chan struct{}, workers)`
  worker pool (verify: `grep -n 'semaphore := make(chan struct{}, workers)' internal/scanner/scanner.go`,
  expected 1 hit ~scanner.go:723). Your new hook runs **synchronously inside that existing per-book
  worker goroutine** — for a multi-file book this means up to N sequential ffprobe subprocess calls (one
  per track, for duration) within that one worker's turn. **Do not spawn additional goroutines per
  track** — that would multiply concurrency beyond the caller's configured `workers` bound, violating
  the CLAUDE.md concurrency rule in spirit (it says bound the *outer* fan-out; this task must not add a
  second, unbounded inner one). Sequential per-track probing inside an already-bounded worker slot is
  the correct, in-scope choice.
- **Rescan behavior, stated honestly:** the incremental scan skip-check (`grep -n
  'Incremental skip check' internal/scanner/scanner.go`, ~scanner.go:748) means an unchanged file on a
  normal scan never reaches either call site at all — it's skipped before extraction runs. So this task
  does **not** retroactively backfill chapters for the existing library on a normal (incremental)
  rescan. A **forced full rescan** (bypassing the mtime/size cache) will populate chapters for every
  existing book on its next pass, using each book's already-persisted `FilePath`/`BookFile` rows — no
  separate backfill op is strictly required for that path. If the owner wants chapters populated
  without waiting for a full rescan, that is a **separate future task**, following the
  `registry.RunItems` bounded-worker-pool backfill pattern already used by
  `internal/plugins/acoustid/backfill.go` (per CLAUDE.md's concurrency rule) — explicitly out of scope
  here; do not build it as part of this task.
- **Your two new call sites fire inside `ProcessBooksParallel`, which many pre-existing scanner tests
  already exercise end-to-end.** Any existing test that drives a book through to a successful save
  (directory-based or single/segment branch) will now also invoke `PersistChaptersForBook` as a side
  effect, even though it isn't testing chapters at all. With a real `*database.PebbleStore` and a
  nonexistent/fake `.m4b` path (common in unit tests that don't ship a real audio fixture), the result
  is a real, cheap, failing `ffprobe`/`exec.Command` subprocess spawn per such test — non-fatal by
  design (this task's error-swallowing), but worth knowing about ahead of time rather than being
  surprised by it. This is exactly why the full-package regression run
  (`go test ./internal/scanner/... -race -count=1`, required below) matters more than usual for this
  task — it's the only way to confirm no *existing* test starts failing, hanging, or meaningfully
  slowing down because of this new, always-on hook.

## Step-by-step (TDD — failing test first)

1. Create `internal/scanner/chapter_persistence_test.go` (package `scanner`) with the file header
   (fresh GUID) and these failing tests, reusing `setupPebbleStore(t)` from
   `save_book_to_database_test.go` (same package, already provides a real `*database.PebbleStore` +
   cleanup):
   - `TestPersistChaptersForBook_SingleFileM4B_UsesEmbeddedChapters` — `store.CreateBook(&database.Book{
     FilePath: <odyssey m4b fixture path>, Title: "The Odyssey"})`, then `SetStore(store)` — this
     package's OWN setter (`getStore()`/`pkgStore`, ~scanner.go:163-176), NOT
     `database.SetGlobalStore` — since `PersistChaptersForBook` reads the store exclusively through
     this package's `getStore()`, matching how `createBookFilesForBook` and every other free helper in
     this file already source their store. Then call
     `PersistChaptersForBook(ctx, book.FilePath, nil)`, then `store.GetChaptersForBook(book.ID)` and
     assert **6** chapters, `chs[0].StartSec == 0`, `chs[0].Title == "Chapter 1:
     odyssey_01_homer_butler_64kb"`, and `chs[5].EndSec` within 0.001s of `9975.428` (the m4b's own
     last-chapter end — NOT the container duration).
   - `TestPersistChaptersForBook_MultiFileMP3s_SynthesizesFromTrackTags` — create a Book, then 6
     `database.BookFile` rows via `store.CreateBookFile` in track order, each `FilePath` one of
     `odyssey_0{1..6}_homer_butler_64kb.mp3`, each `Title` set to the book's own tag value (read it with
     `audioutil`'s already-merged `ProbeDurationSeconds`/a plain tag read, or hardcode the 6 known
     `"The Odyssey: Book 0N"` values verified during this brief's authoring — either is fine, just don't
     invent different text), `TrackNumber` 1-6. Call `PersistChaptersForBook`, assert 6 chapters,
     `chs[0].Title == "The Odyssey: Book 01"` (from the tag, not the filename), `chs[0].StartSec == 0`,
     monotonically increasing offsets, and **`chs[5].EndSec` within 0.001s of `9975.431111`** (the sum
     of track durations) — this is the direct regression test for the §5b duration-authority decision;
     it must NOT be close to `9975.480544` (container) or exactly `9975.428` (m4b chapter end) since
     this book has no m4b/container in this path at all.
   - `TestPersistChaptersForBook_Idempotent_SkipsReExtraction` — seed a sentinel chapter list directly
     via `ps.SaveChaptersForBook(book.ID, []database.Chapter{{ID: 0, StartSec: 1, EndSec: 2, Title:
     "sentinel"}})`, then call `PersistChaptersForBook` for that same book, then assert
     `GetChaptersForBook` still returns the untouched sentinel (proves it skipped re-extraction rather
     than overwriting).
   - `TestPersistChaptersForBook_NoEmbeddedChaptersSingleTrack_NoOp` — a Book whose `FilePath` is one of
     the mp3 fixtures alone (no BookFiles, single-file case, and that mp3 has no embedded chapters —
     already confirmed via `ffprobe -show_chapters` returning an empty array). Assert
     `PersistChaptersForBook` returns `nil` (no error) and `GetChaptersForBook` afterward is
     `(nil, nil)`.
   - `TestPersistChaptersForBook_NonPebbleStore_LogsWarning` — `SetStore(&database.MockStore{})` (this
     mock already exists and is already used by this package's own tests, e.g. `service_test.go` —
     verify: `grep -n "database.MockStore{}" internal/scanner/service_test.go`), call
     `PersistChaptersForBook`, assert a `nil`
     error return (no panic), AND assert `chapterStoreAssertErrCount.Load()` increased by exactly 1 —
     this is the regression test for "warn once, don't silently swallow" from the Background section.
     **`SetStore(nil)` (no store wired at all) is a separate, silent case — do NOT warn on it.** A nil
     store is the normal, expected condition in every other test in this package that doesn't need one
     (mirrors the existing `if store := getStore(); store == nil { return }` guards used throughout
     `scanner.go` for the same reason); only a *non-nil, wrong-concrete-type* store is the genuine
     wiring-regression case worth a log line.
   Use `t.Skip` guards for missing `ffprobe` on PATH and missing fixture files, matching
   `internal/audioutil/chapters_test.go`'s `requireFFprobe`/`requireFixture` pattern (duplicate those
   two tiny helpers locally in this new test file rather than importing test-only helpers across
   packages).
2. Run `go test ./internal/scanner/... -run 'PersistChaptersForBook'` — confirm it fails to compile
   (the function doesn't exist yet).
3. In `internal/scanner/process_file.go`, add the imports `context`, `path/filepath`, `sync/atomic`,
   `time`, `github.com/falkcorp/audiobook-organizer/internal/audioutil`,
   `github.com/falkcorp/audiobook-organizer/internal/database` (alongside the existing `mediainfo`/
   `metadata`/`tag` imports — `logger` is not a new import, `defaultLog`/`logger.Logger` are already
   used package-wide). Add:
   - `const chapterProbeTimeout = 20 * time.Second` (matches `mediainfo.ffprobeDurationTimeout`'s value
     — same rationale: ffprobe only reads container/stream headers, so this is generous while still
     bounding a hung subprocess).
   - `var chapterStoreAssertErrCount atomic.Int64` (package-level, alongside the existing
     `dupLookupErrCount`/`scanCacheUpdateErrCount` counters) and reuse the existing `warnSampled`
     helper to log — on the 1st occurrence and every 1000th after, not every call — when the
     `*database.PebbleStore` type assertion fails. This is a wiring-mismatch warning, not a per-book
     error, so it must not spam the log once per book; `warnSampled` already exists for exactly this.
   - `PersistChaptersForBook(ctx context.Context, bookFilePath string, scanLog logger.Logger) error` —
     implements the algorithm described in the Goal section: `getStore()` → if `nil`, return `nil`
     silently (normal/expected in many tests, mirrors every other `if store == nil` guard in this file)
     → type-assert `*database.PebbleStore` on a **non-nil** store; on failure:
     `warnSampled(&chapterStoreAssertErrCount, scanLog, ...)` then return `nil` (logged, not silent —
     this is the genuine wiring-regression case) → `GetBookByFilePath` (no-op if not found — a real
     "book isn't saved yet" race is a data condition, not a wiring bug, so this one stays quiet) →
     `GetChaptersForBook` already non-empty → no-op (Step 1's idempotency test) → `GetBookFiles` → if
     `len(files) <= 1`, probe the one file's embedded chapters directly (use `files[0].FilePath` if a
     lone `BookFile` exists, else fall back to `dbBook.FilePath` for the true single-file-book case);
     else build one `audioutil.TrackInfo` per file (re-probed `ProbeDurationSeconds`, `Title` from
     `f.Title`, `Filename` from `filepath.Base(f.FilePath)`) and call `audioutil.SynthesizeChapters`.
     Convert `[]audioutil.Chapter` → `[]database.Chapter` (same four fields, direct copy). If the
     result is empty, return `nil` without calling `SaveChaptersForBook` (nothing to persist). Any
     ffprobe failure is logged via `scanLog` (nil-safe) and swallowed — non-fatal to the scan, matching
     the rest of this file's error style.
   - Two small unexported helpers, `probeSingleFileChapters` and `synthesizeMultiFileChapters`, so the
     two extraction paths are independently testable and `PersistChaptersForBook` stays a short
     dispatcher (optional structuring — do this if it keeps the function under ~40 lines, matching this
     file's existing style).
4. In `internal/scanner/scanner.go`, add exactly two call sites (content-matched from the Background
   section, not raw line numbers):
   - Immediately after `createBookFilesForBook(dirPath, nil, scanLog)` (directory-based book branch):
     ```go
     if err := PersistChaptersForBook(ctx, dirPath, scanLog); err != nil {
         scanLog.Warn("chapter persistence failed for %s: %v", dirPath, err)
     }
     ```
   - Immediately after the `if len(books[idx].SegmentFiles) > 1 { createBookFilesForBook(...) }` block
     (i.e. outside that `if`, so it also runs for the genuinely-single-file case where the `if` didn't
     fire), before the scan-cache-update `func() { ... }()`:
     ```go
     if err := PersistChaptersForBook(ctx, books[idx].FilePath, scanLog); err != nil {
         scanLog.Warn("chapter persistence failed for %s: %v", books[idx].FilePath, err)
     }
     ```
5. Run the Step-1 tests again — all must pass for the right reason now.
6. Bump file version headers: `process_file.go` and `scanner.go` get version bumps + `last-edited`
   (guids unchanged); the new test file gets a fresh GUID at `1.0.0`.
7. Add a changelog fragment at `changelog.d/20260730_060700_abs-sync-scanner-chapter-hook.md`:
   ```markdown
   <!-- file: changelog.d/20260730_060700_abs-sync-scanner-chapter-hook.md -->
   <!-- version: 1.0.0 -->
   <!-- guid: <run: uuidgen | tr '[:upper:]' '[:lower:]'> -->
   <!-- last-edited: 2026-07-30 -->

   ### Added

   - **Chapter extraction at scan time (abs-sync Phase 4).** Every scanned book now gets a persisted
     chapter list: single-file books keep their embedded chapters as-is; multi-file books get one
     synthesized chapter per file, titled from each file's own tag. Multi-file chapter boundaries use
     re-probed, unrounded per-track durations (not the stored rounded `BookFile.Duration`), so the
     total matches real Audiobookshelf `startOffset` precision. Idempotent — a rescan skips
     re-extraction for books that already have chapters.
   ```

## How to test

```bash
gofmt -l internal/scanner/process_file.go internal/scanner/scanner.go internal/scanner/chapter_persistence_test.go
# Expected: empty output
go vet ./internal/scanner/...
go test ./internal/scanner/... -run 'PersistChaptersForBook' -race -count=1 -v
go test ./internal/scanner/... -race -count=1
```

Paste the full `-run 'PersistChaptersForBook' -v` output (all 5 tests `PASS`, or `SKIP` with a clear
reason if ffprobe/fixtures are unavailable in the CI runner) and the whole-package `go test` summary in
the PR body. If ffprobe is not installed on the CI runner, the tests must `SKIP`, not silently pass —
confirm the skip reason string appears in the pasted output rather than assuming it.

## Acceptance criteria

- [ ] `PersistChaptersForBook` exists in `internal/scanner/process_file.go`, takes `(ctx, bookFilePath
      string, scanLog logger.Logger)`, and does not change `ProcessFile`'s signature
- [ ] Exactly two call sites added in `scanner.go`, both matching Step 4 verbatim; no call added at the
      AI-batch re-save site (~scanner.go:1146)
- [ ] All 5 tests from Step 1 pass with `-race` (or SKIP with a printed reason if ffprobe/fixtures are
      absent — paste which)
- [ ] The multi-file test's last chapter `EndSec` is within 0.001s of `9975.431111` (sum of track
      durations) and demonstrably NOT close to `9975.480544` (container) — this is the §5b regression
      test and must be a real numeric assertion, not just "no error"
- [ ] No new goroutines spawned per track/file — chapter extraction runs synchronously inside the
      existing bounded worker (verify by inspection in the diff, not just by test passing)
- [ ] A failed `*database.PebbleStore` type assertion is logged via `warnSampled` (not a silent no-op) —
      the 5th test, `TestPersistChaptersForBook_NonPebbleStore_LogsWarning`, asserts the counter
      (`chapterStoreAssertErrCount`) increments on that path
- [ ] The PR body explicitly states, in its own sentence, that this task does NOT implement the §5b
      ≥2s finished-detection tolerance and why (labeled scope decision, not a buried omission)
- [ ] `go test ./internal/scanner/... -race -count=1` passes (no regression in the rest of the package)
- [ ] `gofmt -l` and `go vet` clean on every changed file
- [ ] File headers bumped on every changed file (new test file: fresh GUID, `1.0.0`)
- [ ] Changelog fragment added at the exact path in Step 7

## Commit message

```
feat(abs-sync): extract and persist chapters at scan time

New PersistChaptersForBook (internal/scanner/process_file.go) wires the already-
merged audioutil chapter primitives and TASK-06's Chapter persistence into the
scan pipeline: single-file books keep embedded chapters as-is, multi-file books
get one synthesized chapter per file from cumulative, re-probed (unrounded) per-
track durations, settling the docs/specs/2026-07-29-abs-sync-api-design.md §5b
duration-skew finding in favor of the sum-of-tracks value. Idempotent on rescan.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/abs-sync-scanner-chapter-hook
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "^func PersistChaptersForBook" internal/scanner/process_file.go` already hits, the
transform is already done — run the acceptance checks instead of redoing the work. Rollback = revert
the single commit; the two call sites disappear and chapters simply stop being extracted going forward
(no data corruption — the store-side `chapters:<bookID>` keys already written by a previous run of this
code remain valid and readable, just stale after the revert; TASK-06's `DeleteChaptersForBook`/cascade
is unaffected either way).
