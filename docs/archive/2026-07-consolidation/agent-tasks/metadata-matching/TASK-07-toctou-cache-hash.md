<!-- file: docs/agent-tasks/metadata-matching/TASK-07-toctou-cache-hash.md -->
<!-- version: 1.0.0 -->
<!-- guid: a0a9578d-8118-4051-ba8d-9fbc652198a6 -->
<!-- last-edited: 2026-07-10 -->

# TASK-07 — Close the metadata-cache TOCTOU window via SourceHash validation (INIT-3-T5)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). Config extraction (T1) MUST default to today's literal values — zero behavior change until an operator tunes them.
**File-ownership:** none (`internal/metafetch/cache.go` and `internal/server/server_maintenance_deps.go` are touched by no sibling task).

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · guard-hardening subagent · **Why:** adds a fail-closed guard on a prod apply path — needs careful fail-open/fail-closed reasoning per error case · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-matching-toctou-cache-hash" -b agent/metadata-matching-toctou-cache-hash origin/main
cd "$REPO/.worktrees/metadata-matching-toctou-cache-hash"
git rebase origin/main
```

## Goal

Make the metadata cache's EXISTING `SourceHash` field load-bearing at apply time: add
`ValidateCachedIdentity` + `ErrStaleMetadataCache` to `internal/metafetch/cache.go` (recompute the
existing `hashSearchInputs` over the book's CURRENT fields, compare to the stored hash), and call
it in `ApplyTranscriptionCandidate` BEFORE the existing slot-0 identity re-check (keep both
guards). REUSE `hashSearchInputs` exactly — do NOT invent a second hashing scheme, and do NOT
re-key the cache (keys stay `bookID`; the hash is validated, not keyed).

## Background (verify before editing)

- The window is documented in the code itself: the TOCTOU guard comment block in
  `internal/server/server_maintenance_deps.go` (~382-393) says "Because the metadata cache is
  shared and keyed only by book ID, it can be refreshed between the gate and this call" —
  `ApplyTranscriptionCandidate` (~393) then re-checks only candidate-slot-0 identity.
- `SourceHash` exists on `MetadataCandidateCache` (`internal/database/iface_metadata.go`, ~31-34)
  and is populated at write time in `FetchAndCache` via
  `hashSearchInputs(bookID, query, author, narrator, series)` (`internal/metafetch/cache.go`,
  write at ~87, func at ~121) — but "Diagnostic only in v1": nothing reads it.
- `GetCachedCandidates` (~41) is the shared read path.
- **How to fetch the book's current fields inside `ApplyTranscriptionCandidate`** (the function's
  signature is `(_ context.Context, bookID, gatedTitle, gatedAuthor string) error` — it has no
  book object in scope today, and `server_maintenance_deps.go` makes no store call of its own
  yet): use `book, err := s.Store().GetBookByID(bookID)`. `s.Store()` is the server's
  `database.Store` accessor (`internal/server/server.go`, anchor grep below; the sibling
  `WaitForOp` in this same file already uses it — nil-check the store the same way), and
  `GetBookByID` returns the FULL Pebble row (`internal/database/pebble_store.go`, anchor grep
  below). Field names on the returned `*database.Book` (`type Book struct`,
  `internal/database/store.go`, anchor grep below):
  - title: `book.Title` (`string`)
  - author: `book.Author.Name`, guarded — `book.Author` is a `*database.Author` relation field
    that may be nil; mirror the existing guard shape at
    `internal/server/metadata_batch_candidates.go` (`if book.Author != nil && book.Author.Name != ""`)
  - narrator: `*book.Narrator`, guarded non-nil (`*string`)
  - series: `book.Series.Name`, guarded — `book.Series` is a `*database.Series` relation that
    may be nil
  - nil/absent → `""` in every case, matching what the cache writers pass.
- What the ORIGINAL cache writes hashed (the two production `FetchAndCache` callers — this is
  the answer the trace grep in step 2 yields): the batch path
  (`internal/server/metadata_batch_candidates.go`) passes `(book.Title, authorName, "", "")` —
  narrator and series EMPTY strings; the UI handler path
  (`internal/server/handlers/metadata/handler.go`) passes user-supplied
  `body.Query/Author/Narrator/Series`. Recompute over the book's CURRENT fields as above; a row
  whose stored hash was computed from user-typed inputs that differ from the book's fields will
  fail CLOSED (`ErrStaleMetadataCache`) per the mismatch rule — acceptable and conservative;
  note it in the code comment.
- Fail-open vs fail-closed semantics (state them in code comments AND tests):
  - stored hash EMPTY (legacy row predating the field) → **fail-open**: `slog.Warn` + nil (apply
    proceeds via the existing identity guard);
  - hash MISMATCH → **fail-closed**: return `ErrStaleMetadataCache` (caller already treats
    non-nil error as "skip + log");
  - hash MATCH → nil.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func (mfs \*Service) GetCachedCandidates' internal/metafetch/cache.go
  grep -n 'func hashSearchInputs' internal/metafetch/cache.go
  grep -n 'SourceHash' internal/metafetch/cache.go internal/database/iface_metadata.go
  grep -n 'TOCTOU guard' internal/server/server_maintenance_deps.go
  grep -n 'func (s \*Server) ApplyTranscriptionCandidate' internal/server/server_maintenance_deps.go
  grep -n 'keyed only' internal/server/server_maintenance_deps.go
  grep -n 'func (s \*Server) Store()' internal/server/server.go                 # store accessor for the book fetch (~313)
  grep -n 'func (p \*PebbleStore) GetBookByID' internal/database/pebble_store.go   # full-row by-ID getter (~745)
  grep -n 'type Book struct' internal/database/store.go                        # field names: Title / Author / Narrator / Series (~120)
  grep -n 'Author               \*Author\|Series               \*Series\|Narrator             \*string' internal/database/store.go   # the three guarded fields
  grep -n 'if book.Author != nil && book.Author.Name != ""' internal/server/metadata_batch_candidates.go   # guard shape to mirror
  ```
  Zero hits on any of these = STOP and report drift.

## Step-by-step

1. In `internal/metafetch/cache.go`, add `var ErrStaleMetadataCache = errors.New(...)` and:
   `func (mfs *Service) ValidateCachedIdentity(entry *MetadataCandidateCache, bookID, query, author, narrator, series string) error`
   implementing exactly the three-case semantics from Background (empty → warn + nil; mismatch →
   `ErrStaleMetadataCache` wrapped with book ID; match → nil). It must call the existing
   `hashSearchInputs` — no new hash code.
2. In `ApplyTranscriptionCandidate` (`server_maintenance_deps.go`), after the `GetCachedCandidates`
   read, fetch the book's current fields via `s.Store().GetBookByID(bookID)` and read
   `book.Title` / `book.Author.Name` / `*book.Narrator` / `book.Series.Name` with the nil guards
   exactly as spelled out in Background ("How to fetch the book's current fields") — those are
   the same field kinds the ORIGINAL cache writes hashed (Background lists both `FetchAndCache`
   caller shapes; re-confirm with
   `grep -rn 'FetchAndCache(' internal/ --include='*.go' | grep -v _test` if in doubt). Then call
   `ValidateCachedIdentity` BEFORE the existing slot-0 identity comparison. On error: return it
   (caller treats as skip + log). Extend the existing TOCTOU comment block to describe the added
   hash layer.
3. Purely additive: keep the existing identity re-check untouched; do not modify
   `GetCachedCandidates`' signature or the cache write path.
4. Tests: `TestValidateCachedIdentity` (match / mismatch / legacy-empty-hash) in
   `internal/metafetch/cache_test.go`; an `ApplyTranscriptionCandidate` test in
   `internal/server/` proving (a) a book whose identity mutated after caching is REFUSED, and (b)
   **anti-over-suppression:** an UNCHANGED book still applies successfully with the new guard
   active (test name: `TestApplyTranscriptionCandidateUnchangedStillApplies`).
5. Bump headers on every touched file; keep existing guids.

## How to test

```bash
make ci
# caveat: staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
# you changed; the merge gate is Minimal CI green.
go test ./internal/metafetch/ ./internal/server/ -run 'CachedIdentity|ApplyTranscription' -v
```

## Acceptance criteria

- [ ] `grep -n "ErrStaleMetadataCache" internal/metafetch/cache.go` hits (declaration) and `grep -n "ValidateCachedIdentity" internal/server/server_maintenance_deps.go` hits (call site)
- [ ] Three-case semantics tested: match → nil; mismatch → `ErrStaleMetadataCache`; empty legacy hash → nil + warn (fail-open)
- [ ] `TestApplyTranscriptionCandidateUnchangedStillApplies` green — a known-good unchanged book still applies with the new guard active (anti-over-suppression)
- [ ] Existing slot-0 identity re-check still present (`grep -n "gatedTitle" internal/server/server_maintenance_deps.go` hits)
- [ ] Tests green; vet/lint clean (`make ci`, staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(metafetch): validate cache SourceHash at apply time to close TOCTOU window (INIT-3-T5)

The metadata cache is keyed only by book ID; the apply-time guard in
ApplyTranscriptionCandidate re-checked candidate identity but not whether
the book's search inputs had drifted since the cache write. SourceHash
(stored since v1, diagnostic-only) is now recomputed and compared before
apply: mismatch fails closed, legacy empty-hash rows fail open with a
warning. The existing identity re-check is kept as a second layer.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/metadata-matching-toctou-cache-hash
gh pr create --fill
gh pr merge <number> --rebase
```

(When running under a coordinated sweep, STOP after commit — the coordinator owns push/PR/merge.)

## Idempotency / Rollback

If `grep -n "ErrStaleMetadataCache" internal/metafetch/cache.go` hits, this task is already
applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the
pre-existing slot-0 identity guard remains in place, so apply safety returns exactly to today's
level (no data is touched — the guard only refuses applies, it never mutates the cache).
