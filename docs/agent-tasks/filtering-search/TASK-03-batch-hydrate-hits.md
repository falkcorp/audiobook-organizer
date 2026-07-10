<!-- file: docs/agent-tasks/filtering-search/TASK-03-batch-hydrate-hits.md -->
<!-- version: 1.0.0 -->
<!-- guid: c4faf724-a580-4c57-b87b-ef5316ac0896 -->
<!-- last-edited: 2026-07-10 -->

# TASK-03 — Batch-hydrate Bleve hits via GetBooksByIDs (INIT-4 T3)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). T1/T2 are user-visible correctness fixes — ship first.
**File-ownership:** none

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · store-getter subagent · **Why:** adds a Store interface method (fidelity + mock discipline), behavior must stay invariant · **Depends on:** TASK-02 (shares `internal/audiobooks/service_query.go` — start only after TASK-02's PR merges, branch from fresh origin/main)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/filtering-search-batch-hydrate" -b agent/filtering-search-batch-hydrate origin/main
cd "$REPO/.worktrees/filtering-search-batch-hydrate"
git rebase origin/main
```

## Goal

Replace the per-hit `svc.store.GetBookByID(h.BookID)` loop in `searchWithBleve` with a single
batched store call. Add `GetBooksByIDs(ids []string) ([]Book, error)` to the `BookReader`
interface with a PebbleStore implementation that preserves input order, silently skips
unresolvable IDs, and returns FULL-fidelity rows (the same `book:<id>` JSON point-get that
`GetBookByID` does — never a memdb-slim projection; heavy fields like `AcoustIDFingerprint`
must survive). REUSE `GetBookByID`'s exact read pattern; do NOT add caching, do NOT change
what a hydrated Book contains. (Why an interface method and not a service-local helper:
spec Decision 6 — the store seam is the only place a real Pebble snapshot/iterator
multi-get can later land without touching consumers, and searchWithBleve hydrates up to 10K
hits per per-user-filtered request; the one-mock-method cost is accepted.)

## Background (verify before editing)

- The loop to replace lives in `searchWithBleve` (`internal/audiobooks/service_query.go`):
  `b, _ := svc.store.GetBookByID(h.BookID); if b != nil { books = append(books, *b) }` —
  note the semantics you must preserve: errors and missing rows are skipped, not surfaced,
  and Bleve's relevance order is kept. TASK-02 may have introduced a second hydration site
  in the same function (per-user over-fetch path) — convert EVERY `GetBookByID(h.BookID)`
  occurrence in this function.
- `PebbleStore.GetBookByID` (`internal/database/pebble_store.go`) is a point-get of key
  `book:<id>` + `json.Unmarshal` into `Book`, returning `(nil, nil)` on `pebble.ErrNotFound`.
  Mirror it exactly inside the batch loop.
- `BookReader` interface lives in `internal/database/iface_book.go` and already declares
  `GetBookByID(id string) (*Book, error)`.
- The generated store mock is `internal/database/mocks/mock_store.go`. **Do NOT run an
  unscoped mockery regeneration** — local mockery version drift regenerates every mock in the
  repo (documented footgun; see `docs/MOCKERY_GUIDE.md`). Hand-add the one method following
  the style of the existing `GetBookByID` mock method in that file.
- Concurrency rule check (CLAUDE.md): the batch loop is bounded by the request page
  (≤10000 hits after TASK-02's window), NOT whole-library scale, and each item is a cheap
  local point-get — a sequential loop is correct here. State this in a code comment so the
  next audit doesn't flag it.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'GetBookByID.h.BookID' internal/audiobooks/service_query.go               # loop(s) to replace, >=1 hit
  grep -n 'func.*PebbleStore.*GetBookByID' internal/database/pebble_store.go        # copy-from source, 1 hit
  grep -n 'GetBookByID.id string' internal/database/iface_book.go                   # interface anchor, 1 hit
  grep -n "GetBookByID" internal/database/mocks/mock_store.go                       # mock style to mirror, >=1 hit
  grep -rn "GetBooksByIDs" --include='*.go' .                                       # MUST be 0 hits before you start (see Idempotency)
  ```
  Zero hits on any of the first four = STOP and report; the file has drifted.
  The patterns are deliberately paren-free (`.` wildcards where the source has `(`,
  `)`, or `*`) so the SAME string resolves under both POSIX bash grep (BRE) and
  ripgrep/PCRE-style Grep tools. Do NOT "tighten" them by adding literal parens
  (a PCRE engine reads them as capture groups → 0 hits) or backslash-escaped
  parens (breaks BRE bash grep) — run them exactly as written, in either tool.

## Step-by-step

1. In `internal/database/iface_book.go`, add to `BookReader` directly below `GetBookByID`:
   ```go
   // GetBooksByIDs returns the full Book rows for ids, preserving input
   // order and silently skipping IDs that do not resolve (mirrors
   // GetBookByID's nil-on-not-found). Full fidelity: reads the complete
   // book:<id> row — heavy fields (AcoustIDFingerprint etc.) intact.
   GetBooksByIDs(ids []string) ([]Book, error)
   ```
2. In `internal/database/pebble_store.go`, implement it directly below `GetBookByID`,
   reusing the same key format + not-found handling per item (a plain sequential loop —
   bounded by request page size, see Background; add that comment). Return
   `[]Book{}`-capacity-len(ids), never nil, on success. **Error semantics (spec §C3,
   verbatim contract):** per-item not-found is skipped silently; on the FIRST non-not-found
   read/unmarshal error, STOP and return the rows read SO FAR **alongside** the error —
   `return books, fmt.Errorf(...)`, NOT `return nil, err` — so the caller can serve a
   partial page (document this in the method comment).
3. Hand-add the mock method to `internal/database/mocks/mock_store.go`, mirroring the
   existing `GetBookByID` mock method's expecter/Called style exactly. No mockery run.
4. In `internal/audiobooks/service_query.go` (`searchWithBleve`), collect
   `ids := make([]string, 0, len(hits))` then one `svc.store.GetBooksByIDs(ids)` call per
   hydration site; keep the skip-missing + order semantics (they now live in the store).
   **FAIL-OPEN at the call site (spec §C3 — do NOT propagate the error up):** on a non-nil
   error from `GetBooksByIDs`, `slog.Warn` (with the error + rows-hydrated count) and serve
   the PARTIAL page from the rows that were returned alongside the error. Propagating the
   error would fail the entire search page on a single bad row — a fail-closed behavior
   change from today's silent per-hit skip; the page must shrink LOUDLY, never error out.
5. Purely additive elsewhere: do not touch other `GetBookByID` callers, do not retype
   anything to `BookCore`, do not add the method to any other interface.
6. Edge semantics (in tests too): empty `ids` → empty slice, nil error; duplicate IDs in
   input → duplicated rows out (input-order contract, callers dedupe if they care); an ID
   that fails to unmarshal returns rows-so-far + the error (getter), and `searchWithBleve`
   warns + serves the partial page (never a request failure).
7. Tests — `internal/database/pebble_store_books_by_ids_test.go` (NEW; mirror the setup of
   an existing PebbleStore test — find one: `grep -rln "func TestPebbleStore" internal/database/*_test.go | head -3`):
   order preservation (request [C,A,B] → get [C,A,B]); unknown ID skipped; empty input;
   fidelity: store a Book whose file/fingerprint-adjacent heavy fields are set, read it back
   through the batch getter, assert non-empty; error-alongside-rows: a corrupt row mid-batch
   returns the rows read before it AND a non-nil error. Plus, service-level
   `TestSearchWithBleveHydrationErrorPartialPage` (spec Testing table): mock
   `GetBooksByIDs` returning (partial rows, error) → `searchWithBleve` serves the partial
   page + warn logged, no error returned to the caller (fail-open, §C3 parity). Existing
   search-path service tests stay green (behavior invariant). Anti-over-suppression: N/A
   (no filter/guard added — batch of identical reads).
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
go test ./... -short   # FULL suite mandatory: Store interface changed (store-getter rule — mock consumers fail loudly here, not in a subset)
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "GetBooksByIDs" internal/database/iface_book.go internal/database/pebble_store.go internal/database/mocks/mock_store.go internal/audiobooks/service_query.go` hits in all FOUR files
- [ ] `grep -n 'GetBookByID.h.BookID' internal/audiobooks/service_query.go` returns 0 hits (same paren-free pattern as the Re-verify block — a parenthesized pattern would return 0 in a PCRE Grep tool even if the loop were still present, vacuously passing this check)
- [ ] Order/skip/fidelity tests green; empty-input returns `[]Book{}` not nil
- [ ] Fail-open: getter returns rows-so-far alongside a non-not-found error; `searchWithBleve` warns + serves the partial page (`TestSearchWithBleveHydrationErrorPartialPage` green) — the search request never fails on a single bad row
- [ ] `git diff --stat` shows NO other mock files touched (scoped mock edit)
- [ ] Anti-over-suppression: N/A
- [ ] Tests green (`make ci` + full `go test ./... -short`); vet/lint clean
- [ ] File headers bumped on every changed file

## Commit message

```
perf(search): batch-hydrate Bleve hits with GetBooksByIDs (INIT-4 T3)

searchWithBleve fetched every hit one-by-one via GetBookByID. Add an
order-preserving, skip-missing, full-fidelity batch getter to BookReader
(PebbleStore impl mirrors the single-get's book:<id> point read) and use
it for hit hydration. Behavior invariant; one call per page instead of N.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/filtering-search-batch-hydrate
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "GetBooksByIDs" internal/database/iface_book.go` hits, this task is already
applied (additive polarity: presence of the new interface method) — run the acceptance checks
instead of re-applying. Rollback = revert the commit; the per-hit loop returns and no data,
schema, or key format is touched (the getter only reads existing `book:<id>` rows).
