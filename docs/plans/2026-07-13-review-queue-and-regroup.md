<!-- file: docs/plans/2026-07-13-review-queue-and-regroup.md -->
<!-- version: 1.2.0 -->
<!-- guid: 3d9f2a6c-7e14-4b8a-9c2d-5f1e6a3b7c40 -->
<!-- last-edited: 2026-07-14 -->

# PLAN — Universal Review Queue + Shattered-Book Regroup Op

**Status:** Awaiting approval. NO feature code written until the user signs off.
**Author decisions captured:** 2026-07-13 (see "Locked decisions" — all via AskUserQuestion).

---

## Goal

Two independently-shippable tracks, sequenced so the review queue ships already
populated with real data from the regroup op:

- **Track A — Universal Review Queue.** A single, glanceable home for everything the
  system has flagged for a human decision. A global banner ("You have N items to
  review") + a left-nav `/review` panel. v1 producer = the regroup op only, so the
  count is meaningful from day one (starts at 0, grows only with intentional holds).
- **Track B — Regroup Shattered Books.** A new maintenance op
  `maintenance.regroup-shattered-ai` that groups the thousands of single-file "books"
  (one track = one book, from the broken iTunes import) back into real multi-file
  audiobooks, using the **folder path as the identity signal**. v1 is **regex-only**
  (no GPU dependency); ambiguous folders are *held for review* rather than
  AI-classified. AI enrichment is a fast-follow once the GPU box serves Ollama.

The regroup op is the first *producer* into the review queue — it does not invent its
own private "held" list.

---

## Locked decisions (from the user, 2026-07-13)

| # | Decision | Choice |
|---|----------|--------|
| 1 | Review count semantics | **Regroup holds only, v1.** Count starts at 0; dedup/metadata opt in later. Never raw backlogs. |
| 2 | Banner style | **Single aggregate** — "You have N items to review" (breakdown lives inside the panel). |
| 3 | Approve action | **Apply immediately** (individual) + **grouped bulk actions**. |
| 4 | Hold volume handling | **Grouped bulk actions** — bucket holds by shape; approve/reject a whole class or filtered subset in one click. |
| 5 | Rollout | **Whole-library dry-run first.** Run #1 writes **zero** book/file changes; auto-apply-confident is a *separate later toggle*, off in run #1. |
| 6 | AI tier | **Regex-only v1**; ambiguous → held (no AI). AI enrichment = fast-follow when Ollama is serving. |
| 7 | Cover recovery (folder.jpg→cover) | **Separate later phase/PR.** Not in the grouping op. |
| 8 | Abridged + Unabridged in one folder | **2-book version group**, both visible (primary = Unabridged). Via `UpdateBook`, NOT `MergeBooks`. |
| 9 | Anthology / trilogy | **N separate books** (one per distinct work). In v1 (regex), these are *held for review* — regex can't reliably split them; AI does that later. |

---

## Infra reality (verified this session — informs Track B)

- **Whisper** runs on the GPU box (172.16.3.22) via `WHISPER_REMOTE_URL=http://172.16.3.22:19847` — healthy, resident ~6 GB on the 8 GB RTX 2070 SUPER.
- **Ollama** is installed on the box (v0.31.1) but **not running / fails to start headless**
  (`Unable to init instance … timed out`), and prod sets **no** `ai_backend.local_base_url`
  (falls back to the checked-in placeholder `192.168.0.20:11434`, which is dead from the server).
- ⇒ The LLM→GPU path is **non-functional today**. This is *why* v1 is regex-only and the AI
  tier is deferred. No code in v1 depends on the box. When the AI tier lands, it will
  `ProbeOllamaAvailable` and **degrade gracefully to regex-only** if the endpoint is down.

---

## Track A — Universal Review Queue

### A1. Backend: generic review-item store (greenfield)

New store (mirror the dedup `Status`/secondary-index pattern; do **not** overload the dedup store).

`ReviewItem` (Pebble-backed, new keyspace `review_item:*`):
```
ID          string     // ULID
Kind        string     // "regroup.multidisc" | "regroup.anthology" | "regroup.version-group" | "regroup.ambiguous"
DedupKey    string     // STABLE: sha/normalized (Kind + folder-ref) — upsert target
FolderRef   string     // grandparent folder path this hold is about
Status      string     // "pending" | "approved" | "rejected" | "applied"  (stringly-typed, matches house style)
Summary     string     // one-line human label
Payload     string     // JSON: folder path, file listing, proposed action, member book IDs, confidence, derived title, (AI reasoning later)
CreatedAt   time.Time
UpdatedAt   time.Time
```
- **Idempotency (advisor-flagged):** producers UPSERT keyed by `DedupKey = (Kind, FolderRef)`.
  Re-running the dry-run must not pile up duplicates. Status is preserved on upsert (a
  re-scan does not un-reject a rejected item).
- **Dismiss persistence:** `rejected` is remembered (keyed by folder) so re-scans don't resurface it.
- Secondary index `review_item:status:<status>:<id>` for fast pending-count + list-by-status
  (mirror `dedupStatusIdxKey` / `WriteCandidateStatusIndexRow` at `internal/database/embedding_store.go:449,731`).
- Count = number of `pending` items (index scan / maintained counter).

Files: `internal/database/review_store.go` (new), `internal/database/iface_review.go` (new
interface), wire into PebbleStore + the store mock (`mock_store.go`).

### A2. Backend: HTTP API

Register on the protected group (`s.perm(auth.PermLibraryView)`), house envelopes from `internal/httputil`:
- `GET /api/v1/review/count` → `RespondWithOK(gin.H{"count": N, "byKind": {...}})`.
- `GET /api/v1/review/items?status=pending&kind=&limit=&offset=` → `RespondWithList(items, count, limit, offset)`.
- `POST /api/v1/review/items/:id/approve` → applies immediately (calls the regroup apply-one path), sets `applied`.
- `POST /api/v1/review/items/:id/reject` → sets `rejected`.
- `POST /api/v1/review/bulk` → `{action: approve|reject, filter: {kind, ids?}}` → grouped bulk action (decision #4).

Files: `internal/server/handlers/review/handler.go` (new), `internal/server/wire_review_routes.go` (new),
registered in the router setup alongside `wire_dedup_routes.go`.

### A3. Frontend

- **Global banner** (single aggregate, decision #2): new `ReviewBanner.tsx` rendered in
  `MainLayout.tsx` above `{children}` (so it shows on every route, unlike the Dashboard-only
  AnnouncementBanner). Hidden when count == 0. Clicking → `/review`.
- **Live count**: new `useReviewStore` (zustand), mirroring `useOperationsStore` — initial REST
  fetch + refresh. v1 = poll `/review/count` on mount + interval (SSE event optional fast-follow).
- **Left-nav entry**: add `{ text: 'Review', icon, path: '/review' }` to `Sidebar.tsx` (after Dedup),
  with a `<Badge>` count copied from `OperationsIndicator`.
- **`/review` page** (`web/src/pages/ReviewQueue.tsx`, lazy-imported + routed in `App.tsx`):
  holds grouped by `Kind` (decision #4) with per-group counts, filters, and
  approve/reject on individual items **and** whole buckets/filtered subsets.

### A4. Track A test strategy
- Go: store round-trip + upsert-idempotency (same DedupKey twice → 1 row, status preserved);
  status-index count correctness; handler tests for count/list/approve/reject/bulk with the store mock.
- Frontend: Vitest for the store (count refresh) + banner visibility (0 → hidden, N → shown) + bulk action wiring.

---

## Track B — Regroup Shattered Books (`maintenance.regroup-shattered-ai`)

### B0. Op registration
Add `p.regroupShatteredAIDef()` to the `defs` slice in
`internal/plugins/maintenance/plugin.go` (~line 95); it's registered by the existing
`r.RegisterOp(d)` loop. Copy the shape of `fs_regroup_xml.go`:
`sdk.OperationDef{ID:"maintenance.regroup-shattered-ai", Capabilities:[CapLibraryRead(,CapLibraryWrite when apply)], ResumePolicy:ResumeDrop, ConcurrencyKey:..., Timeout:120m, Run:p.runRegroup}`.
Params struct `regroupParams{ Apply bool json:"apply" }` — **default false = dry-run** (safe).
(Note: run #1 = dry-run regardless; apply is a later toggle per decision #5.)

### B1. Detection (read-only) — the regex/deterministic tier
- Enumerate the full library with `ListBookIDs()` → `registry.RunItems(Concurrency: NumCPU())`
  → `GetBookByID` + `GetBookFiles` per item (the memdb-cap-safe pattern). CPU-bound fan-out.
- Group single-file books by **grandparent folder** (the identity signal). Reuse/extend the pure
  `GroupShatteredBooks` model in `internal/itunes/service/fs_regroup.go` — but broaden the shape
  detector beyond the current `^(.*) - (\d+)$` (which heals 0/44,327) to recognize:
  - **A. flat multi-track** (many audio files, one folder) → 1 book, N files — **confident**.
  - **B. multi-disc** (`Disc N/` subfolders) → 1 book, N files — **confident**.
  - **C. version-group** (Abridged + Unabridged markers) → **hold** (`regroup.version-group`).
  - **D. anthology / trilogy** (multiple distinct-title markers) → **hold** (`regroup.anthology`).
  - **F. genuine single file** → leave alone (no action).
  - anything unclassifiable → **hold** (`regroup.ambiguous`).
- Title derivation for the survivor: strip `NN - ` prefix and `(era/year)` suffix; author from
  tags/metadata, never the path.

### B2. Dry-run output (run #1 — writes ZERO book/file changes)
**Read-only w.r.t. books/files; DOES write review-queue rows — that is the point.**
- Confident A/B groups → written to the review queue too in run #1 (as `pending`), *not*
  auto-applied. (Auto-apply-confident is a separate toggle, off in run #1 — resolves the
  decision-5-vs-"auto-apply-confident" tension the advisor flagged.)
- C/D/ambiguous → `pending` holds with full payload (folder, listing, proposed action, member IDs, confidence).
- A `RECONCILE:` log line accounting for every book (mirror fs_regroup_xml.go), + reporter progress.
- Net effect of run #1: the review queue fills with real, deduped holds; the library is untouched.

### B3. Apply path (later toggle / driven by review approvals — decision #3 "apply immediately")
Approving an item in the panel (or enabling the confident-auto-apply toggle) executes the write
**for that group**, taking the shared lock and using the safe primitives:
- **Collapse N single-file books → 1 multi-file** (shapes A/B): `merge.CombineBooks(memberIDs, survivorID, override)`.
  Materializes virtual `Book.FilePath` into real BookFiles, moves files via `MoveBookFilesToBook`,
  **hard-deletes** absorbed shells (files stay on disk → rescan-recoverable — this is what makes
  it safe enough to auto-apply), then `RecomputeBookAggregates(survivorID)`.
- **Version group** (shape C, decision #8): set `VersionGroupID` + `IsPrimaryVersion` (Unabridged=primary)
  on **both** books via `UpdateBook`. **Do NOT** call `MergeBooks` (it soft-deletes the loser). Reuse
  the group-ID-reuse logic from `merge/service.go:144-154`.
- **Anthology** (shape D): split into N books — v1 holds these for review; the actual split write
  lands with the AI tier (which identifies sub-work boundaries). Until then, approving an anthology
  hold is a no-op-with-note or deferred.
- **Concurrency/ordering:** partition apply by `FolderRef`/survivor so two workers never heal the
  same group; take `mergeSerializeMu` (shared with `dedup.MergeBooks`) on every merge-family write.

### B4. Data-loss safety rules (from the store audit — carry verbatim into the apply path)
1. **Source every book/file to be written from FULL getters** — `GetBookByID` / `GetBooksByIDs` /
   `GetBookFiles`. **Never** write back from `GetAllBooks`(memdb) or `GetAllBookFilesCore` (they
   strip Author/Series/Description/BookSig* and AcoustIDFingerprint/segments respectively).
2. Move files with `MoveBookFilesToBook`, not reconstructed BookFile writes (the BookFile PK
   embeds bookID; re-keying is what that function does).
3. `UpdateBook` has the nil-preserve guard for the 9 stripped fields; still re-fetch survivor via
   `GetBookByID` before applying overrides (as CombineBooks does at service.go:357).
4. `RecomputeBookAggregates(survivorID)` after any file move to fix Duration/FileSize sums.
5. **Cover orphaning:** covers are keyed by `covers/{bookID}.*`. When a shell is deleted/absorbed,
   its cover orphans. v1 does not touch covers (decision #7); the cover-recovery phase will handle
   copy-to-survivor.

### B5. Track B test strategy
- Pure grouping: table tests over the broadened shape detector (flat / multi-disc / version-group /
  anthology / single) — no DB.
- Dry-run: asserts ZERO book/file mutations, N review rows created, upsert idempotent on re-run.
- Apply (`-race`): CombineBooks collapse invariants (survivor file count, shells gone, aggregates
  recomputed, fingerprints intact); version-group both-visible + primary correct; partition/lock
  race test (two workers, disjoint folders, no double-heal). Extend the existing data-loss
  invariant suite (#1941) rather than a parallel one.

---

## PR sequence (each independently green + mergeable)

1. **PR-A1** Review-queue backend (store + interface + mock + API + wire routes) — additive, no behavior change.
2. **PR-A2** Review-queue frontend (banner + store + sidebar + /review page + grouped bulk actions).
3. **PR-B1** Regroup op: registration + broadened shape detector (pure, unit-tested) + **dry-run only**,
   producing review-queue holds. No apply path. (This is what populates the queue with real data.)
4. **PR-B2** Regroup apply path (CombineBooks collapse + version-group) wired to review-approve.
   `-race` invariant tests. **✅ BUILT — branch `feat/regroup-apply-path-b2` (PR open, UNMERGED).**
   Kept the global apply switch as `config.review_apply_enabled` (**default OFF**): even with the
   code deployed, approving a hold records the decision but NEVER merges while the switch is off —
   everything stays visible in the review pane (per the user's explicit "no auto-merge until the
   big switch" requirement). The gate lives in the review handler (producer-agnostic), read at
   approve time. Multidisc uses a **nil** CombineBooks override (survivor row never rewritten);
   version-group uses re-fetch-and-patch UpdateBook. Anthology/ambiguous stay handler-less.
   Merging+deploying + flipping the switch is the gated prod-apply decision.
5. **Fast-follow (separate PRs, not in this plan's critical path):**
   - AI enrichment tier (`OpenAIParser.ClassifyFolderShape`, batched, small model, `ProbeOllamaAvailable`
     graceful-degrade) — **blocked on Ollama serving on the box.**
   - Cover recovery (folder.jpg/cover.jpg → `covers/{bookID}`, priority rules) — own PR.
   - Dedup REVIEW-band as a second review-queue producer (opt-in).

---

## Test / verification strategy (overall)
- `make ci` gate per PR; `-race` on all apply/merge tests.
- Dry-run verified against prod (read-only) before any apply toggle is enabled.
- Follow the STOREFID lesson: after any store-getter touch, run the **full** `go test ./... -short`,
  not a subset (old-func mocks vacuous-pass elsewhere).

## Rollback
- Track A: additive (new store keyspace, new routes, new UI). Rollback = revert PRs; no data migration.
  Review rows are advisory; deleting the keyspace loses only the queue, not library data.
- Track B dry-run: read-only on books; rollback = revert + delete `review_item:*` rows.
- Track B apply: CombineBooks hard-deletes shells but **leaves files on disk** → a rescan re-imports
  them (the pre-regroup shattered state is reconstructable). Version-group writes are reversible by
  clearing `VersionGroupID`. No apply runs until dry-run numbers are reviewed and the toggle is
  explicitly enabled.

## Out of scope / explicitly deferred
- AI classification tier (needs Ollama serving — infra gap above).
- Cover-art recovery (own phase).
- Dedup/metadata as review-queue producers (opt-in later; keeps the v1 count meaningful).
- INIT-7, INIT-8 (parked).
