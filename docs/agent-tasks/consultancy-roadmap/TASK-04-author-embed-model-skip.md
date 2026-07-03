<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-04-author-embed-model-skip.md -->
<!-- version: 1.0.0 -->
<!-- guid: db161153-79d0-4a29-bd0d-9a5dab066737 -->
<!-- last-edited: 2026-07-03 -->

# TASK-04 — Model-aware re-embed skip in `EmbedAuthor` + `EmbedBooksAsync` (consultancy-roadmap: DEDUPC-1 / TOGGLE-2)

**Priority:** P0 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-04-author-embed-model-skip" -b agent/cr-04-author-embed-model-skip origin/main
cd "$REPO/.worktrees/cr-04-author-embed-model-skip"
git rebase origin/main
```

## Goal

PR #1738 made the **book** re-embed cache-skip model-aware
(`prepBookEmbed` now requires `de.embeddingModelMatches(existing.Model)` in
addition to a `TextHash` match, so a backend cutover — e.g.
text-embedding-3-large → bge-m3 — forces a re-embed even when the book's text
is unchanged). `EmbedAuthor` and `EmbedBooksAsync` were never given the same
guard: they skip on `TextHash` equality alone. Since an author's name almost
never changes, every author embedding is permanently stranded on whatever
model created it — after the OpenAI→Ollama cutover, all existing author
vectors stay 3072-dim text-embedding-3-large forever while new authors get
1024-dim bge-m3 vectors. `CheckAuthor`'s similarity search then compares
mismatched-dimension vectors (linear-scan cosine returns 0/garbage; HNSW on
mixed dims is the class of panic #1741 now recovers as an error), so author
dedup Layer 2 silently returns nothing. This is DEDUPC-1 (severity high,
effort low) and the author-path half of TOGGLE-2 in the consultancy findings.

Apply the identical `embeddingModelMatches` guard used in `prepBookEmbed` to
both `EmbedAuthor`'s and `EmbedBooksAsync`'s cached-skip checks. `EmbedBooksAsync`
is the OpenAI Batch API path (`/v1/batches`); it is currently inert under an
Ollama backend and is being gated on backend==openai separately in a later
task (see TASK-05/TASK-10 in this same roadmap — do NOT add that gating here).
Fix its model-skip anyway, for correctness and because the same-shaped bug is
present regardless of whether the path is currently reachable.

## Background (verify before editing)

- `internal/dedup/engine.go` defines:
  - `embeddingModelMatches(storedModel string) bool` — compares a stored
    embedding's `Model` field against `de.embedClient.Model()`; returns `true`
    (no forced churn) when `de.embedClient == nil`.
  - `prepBookEmbed` — the reference implementation of the fix. Its cached-skip
    condition is `existing.TextHash == hash && de.embeddingModelMatches(existing.Model)`.
  - `EmbedAuthor(ctx context.Context, authorID int) error` — its cached-skip
    condition is currently `existing.TextHash == hash` only (no model check).
  - `EmbedBooksAsync(ctx context.Context) (batchID string, count int, err error)`
    — its per-book cached-skip inside the build-items loop is currently
    `existing.TextHash == hash` only (no model check).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (de \*Engine) prepBookEmbed\|func (de \*Engine) embeddingModelMatches\|func (de \*Engine) EmbedAuthor\|func (de \*Engine) EmbedBooksAsync" internal/dedup/engine.go
  ```
  Confirm the exact cached-skip lines in each function (do not assume the line
  numbers below — this brief was written 2026-07-03, verified against
  `engine.go` at that commit, showing:
  `prepBookEmbed` skip at line 2094 (`existing.TextHash == hash && de.embeddingModelMatches(existing.Model)`),
  `EmbedAuthor` skip at line 2244 (`existing.TextHash == hash` — no model check),
  `EmbedBooksAsync` skip at line 2308 (`existing.TextHash == hash` — no model check)):
  ```bash
  grep -n "existing.TextHash == hash" internal/dedup/engine.go
  ```
  If any of the three matches above already includes `embeddingModelMatches`,
  stop and re-read the Idempotency section before making further changes —
  that function's fix may already be shipped.

## Step-by-step

1. Open `internal/dedup/engine.go`. Re-verify anchors with the greps above.
2. In `EmbedAuthor`, change:
   ```go
   existing, err := de.embedStore.Get("author", entityID)
   if err == nil && existing != nil && existing.TextHash == hash {
   ```
   to:
   ```go
   existing, err := de.embedStore.Get("author", entityID)
   if err == nil && existing != nil && existing.TextHash == hash && de.embeddingModelMatches(existing.Model) {
   ```
   Leave the rest of the function (the `mirrorAuthorToChromem` call, the
   `return nil`, and the fallthrough embed-and-upsert path) untouched — the
   existing fallthrough already re-embeds and re-upserts with the current
   `de.embedClient.Model()`, so no other change is needed for the re-embed to
   pick up the new model.
3. In `EmbedBooksAsync`, change the per-book skip inside the `for _, book :=
   range books` loop:
   ```go
   existing, getErr := de.embedStore.Get("book", book.ID)
   if getErr == nil && existing != nil && existing.TextHash == hash {
       continue
   }
   ```
   to:
   ```go
   existing, getErr := de.embedStore.Get("book", book.ID)
   if getErr == nil && existing != nil && existing.TextHash == hash && de.embeddingModelMatches(existing.Model) {
       continue
   }
   ```
   Do not add backend gating (openai-only) here — that is a separate,
   later-wave task. This change only fixes the model-skip bug.
4. Do not touch `embeddingModelMatches` itself, `prepBookEmbed`, or any other
   function.
5. Extend `internal/dedup/embed_model_cutover_test.go` (existing pattern:
   `TestEmbedBooks_ReembedsOnModelChange`, using `setupTestEngine`,
   `ai.NewEmbeddingClientWithOptions`, and `SetRawEmbedForTest`) with a new
   test `TestEmbedAuthor_ReembedsOnModelChange`:
   - Set `mock.GetAuthorByIDFunc` to return a fixed `*database.Author{ID: <n>,
     Name: "A Real Author"}` for the target ID.
   - Embed once with `clientA` (model "model-A") via `engine.EmbedAuthor(ctx,
     authorID)`; assert the stored embedding row (`es.Get("author",
     entityID)`, `entityID = strconv.Itoa(authorID)`) has `Model == "model-A"`.
   - Switch `engine.embedClient` to `clientB` (model "model-B", same author
     name so `TextHash` is unchanged); call `EmbedAuthor` again; assert
     `clientB`'s raw-embed fake was invoked (re-embed happened, not skipped)
     and the stored row now has `Model == "model-B"`.
   - Sanity: call `EmbedAuthor` a third time at the same model/content;
     assert the raw-embed fake is NOT invoked again (still skips on a true
     cache hit).
   This mirrors `TestEmbedBooks_ReembedsOnModelChange` exactly, adapted for
   the author entity type.
6. Optionally add a lighter analogous case for `EmbedBooksAsync` if it fits
   cleanly with the existing mock surface (`mock.GetAllBooksFunc`,
   `mock.GetAuthorByIDFunc`, `mock.GetSeriesByIDFunc`); this is not required
   for acceptance if it would require substantially new test scaffolding — the
   `EmbedAuthor` test is the load-bearing regression test since the advisor
   pass identified `EmbedAuthor` as the live exposure (`EmbedBooksAsync` is
   currently inert under Ollama).
7. Bump the file header (version bump + `last-edited` date) on every file you
   touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/dedup/... -run 'ReembedsOnModelChange|EmbedAuthor|EmbedBooksAsync' -count=1 -v
go test ./internal/dedup/... -count=1
go vet ./internal/dedup/...
```

## Acceptance criteria

- [ ] `EmbedAuthor`'s cached-skip condition includes
      `de.embeddingModelMatches(existing.Model)` in addition to the existing
      `TextHash` check.
- [ ] `EmbedBooksAsync`'s per-book cached-skip condition includes
      `de.embeddingModelMatches(existing.Model)` in addition to the existing
      `TextHash` check.
- [ ] No backend/openai gating was added to `EmbedBooksAsync` in this change
      (that belongs to a separate task).
- [ ] `embeddingModelMatches` and `prepBookEmbed` are unmodified.
- [ ] New test `TestEmbedAuthor_ReembedsOnModelChange` (or equivalently named)
      proves: (a) same model + same content → skip (no re-embed call), (b)
      different model + same content → forced re-embed, stored `Model`
      updated to the new client's model.
- [ ] `go test ./internal/dedup/...` is green; `go vet ./internal/dedup/...`
      is clean; `go build ./...` succeeds.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(dedup): make EmbedAuthor and EmbedBooksAsync re-embed skips model-aware (DEDUPC-1)

prepBookEmbed's cached-skip was fixed in #1738 to also check
embeddingModelMatches, forcing a re-embed on a backend/model cutover even
when the text hash is unchanged. EmbedAuthor and EmbedBooksAsync still
skipped on TextHash alone, so after the OpenAI-to-Ollama cutover every
author embedding stayed permanently stranded on the old 3072-dim
text-embedding-3-large vectors (author names rarely change), silently
degrading author dedup Layer 2 to mixed-dimension comparisons that score
zero or panic. Apply the same guard used in prepBookEmbed to both.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-04-author-embed-model-skip
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `EmbedAuthor`'s and `EmbedBooksAsync`'s cached-skip conditions already
include `de.embeddingModelMatches(...)`, this task is done — verify with:
```bash
grep -n "existing.TextHash == hash" internal/dedup/engine.go
```
and confirm every match is followed by `&& de.embeddingModelMatches(...)`. If
only one of the two functions is missing the guard, apply the fix to that
function only and note in the PR description which one was already fixed.
Rollback = revert the commit; `prepBookEmbed`'s existing fixed behavior is
untouched by this change and remains in effect either way. This task does
**not** trigger a production author re-embed pass — that is an explicit
follow-on operational step (running `dedup.embed-scan` or equivalent against
authors) to be done separately, after this code fix merges and deploys, not
as part of this task's acceptance criteria.
