<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-001-add-a-short-ttl-cache-to-the-search-branch-of-ge.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0b3dce4b-d8f5-4473-a427-8dfde63b9105 -->
<!-- last-edited: 2026-09-02 -->

# TASK-001 — Add a short-TTL cache to the search branch of GetAudiobooksWithTotal (explicit first-cut, defer full dirty-set wiring) (SEARCH-CACHE)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — service_query.go: searchWithBleve L166 has no cache read/write; listCache hits only L231/264, inside the non-search pushdown branch; no 'search:' key. Recommendation: keep - depends on the same invalidation gap as TASK-023 (MERGE-CACHE-EVICT), which the brief itself flags.

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · audiobooks subagent · **Why:** Cache-key composition must be exhaustive (query, limit, offset, UserID-when-per-user-active, sort field/direction, every post-filtering ListFilters value) or it silently serves wrong results across users — needs care enumerating every filter that participates in post-filtering, not just the obvious ones. · **Depends on:** TASK-023 · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 1980 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEARCH-CACHE**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-001-add-a-short-ttl-cache-to-the-search-branch-of-ge" -b agent/audiobooks-001-add-a-short-ttl-cache-to-the-search-branch-of-ge origin/main
cd "$REPO/.worktrees/audiobooks-001-add-a-short-ttl-cache-to-the-search-branch-of-ge"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a short-TTL (30-60s, pick 45s as a documented middle value unless the coordinator prefers otherwise) read-through cache to the search branch of GetAudiobooksWithTotal, keyed by every value that affects the result set: query string, limit, offset, UserID (only when PerUserFilters is active), sort field, sort direction, and every ListFilters value that participates in post-filtering (LibraryState, Tag/Tags, FieldFilters, IsPrimaryVersion, fingerprint status/coverage bounds). Log the TTL choice explicitly as a documented first cut, per the item's own explicit permission for this shortcut, deferring full search-index-dirty-set wiring (which would tie into internal/server/search_reconciler.go) to a follow-up.

## Background (verify before editing)

- internal/audiobooks/service_query.go:104-140's search branch computes `fetchLimit, fetchOffset` (over-fetching to `searchPostFilterWindow` when post-filters are active), calls `svc.searchWithBleve(search, fetchLimit, fetchOffset, f.UserID)`, and this whole expensive path currently re-runs on every keystroke-debounced request with zero caching.
- This is explicitly the SAME invalidation gap as MERGE-CACHE-EVICT (L1627 in this scope) — a cached search result that outlives an edit/merge/delete would show books that no longer exist, which is the identical 'I merged these and still see two copies' symptom. The item explicitly says 'Do NOT cache before deciding invalidation' and then itself authorizes the short-TTL path as the decided answer for a first cut.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'listCache' internal/audiobooks/service_query.go   # hits at L197 (Get) and L203 (Set), inside the non-search branch — non-search branch already has a working cache pattern to mirror
  sed -n '104,140p' internal/audiobooks/service_query.go | grep -c 'listCache\|Cache'   # 0 — search branch has zero cache interaction
  grep -n 'PerUserFilters\|hasPerUser' internal/audiobooks/service_query.go   # hits confirming per-user filters are applied after the Bleve call, not pushed down into it — per-user post-filtering happens after Bleve returns, so the cache key must include UserID/filters, not just the query string
  ```

### Reuse — don't invent

- Use `svc.listCache (existing cache instance, already used by the non-search branch)` in `internal/audiobooks/service_query.go` (verify: `grep -n 'listCache' internal/audiobooks/service_query.go`) — do NOT write a parallel helper.
- Use `the existing non-search cache-key convention `all:<limit>:<offset>:p=…:sb=…:asc=…:noq=…` (model for the new search cache key shape)` in `internal/audiobooks/service_query.go` (verify: `grep -n '"all:"' internal/audiobooks/service_query.go`) — do NOT write a parallel helper.

## Step-by-step

1. Build the cache key from ALL of: the raw search query string, limit, offset, f.UserID (only when hasPerUser), sort field/direction, and every ListFilters field participating in post-filtering (LibraryState, Tag/Tags, FieldFilters, IsPrimaryVersion, fingerprint status/coverage bounds).
2. Route the key through svc.libGen.Key(...) exactly as the non-search branch does at service_query.go:191-194, so the search cache is invalidated immediately on any book create/update/delete/merge, not only after the TTL elapses.
3. Add a cache check at the top of the search branch BEFORE calling searchWithBleve: if a fresh (within TTL) entry exists for the composed generation-scoped key, return it directly.
4. Add a cache write after searchWithBleve and post-filtering/pagination complete, storing the FINAL post-filtered/paginated result keyed by the same generation-scoped key, with an explicit 45s TTL layered on top of the generation scoping (belt-and-suspenders for staleness within a single generation, e.g. slow-changing external metadata).
5. Document at the write site that generation-scoping (not just TTL) is what actually protects against the merge/delete staleness class; the TTL is a secondary bound.
6. When the truncated-window warning (~L135-138) fires, do NOT cache that result.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_001.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Cache key must NOT be just the query string — see verified_anchors above; the per-user test is the concrete proof this was done correctly.
- TTL expiry mid-flight: a request arriving exactly at TTL boundary should simply miss and refetch, no special handling needed.
- The over-fetch truncation warning case must not be cached (see step 5).

## Tests

- internal/audiobooks/service_query_test.go — same query/limit/offset/filters twice within the TTL window returns a cached (identical) result without a second Bleve call (mock/count the underlying searchWithBleve invocations).
- Different UserID with PerUserFilters active, same query/limit/offset — MUST produce a cache MISS (different key), proving two users never share a cached result when per-user filtering is active. This is the anti-suppression-equivalent test for this item: without it, a wrong cache key would silently leak one user's filtered results to another.
- A result flagged as a lower-bound (post-filter window exhausted) must not be served from cache on a subsequent identical request within the TTL — assert the second call still triggers a fresh Bleve call in that case.

Anti-over-suppression test: `TestSearchCache_DifferentUserID_CacheMiss (different UserID with per-user filters active must never reuse another user's cached result)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/audiobooks/...` passes including the new cache-key tests.
- [ ] Manual: repeat the same search request twice within 45s and confirm (via added instrumentation/log count) the second request does not re-run searchWithBleve.
- [ ] Anti-over-suppression test: `TestSearchCache_DifferentUserID_CacheMiss (different UserID with per-user filters active must never reuse another user's cached result)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_001.md`.

## Commit message

```
feat(audiobooks): Add a short-TTL cache to the search branch of GetAudiobooksW (SEARCH-CACHE)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'listCache' internal/audiobooks/service_query.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: a wrong cache key (missing UserID or a post-filter field) would silently serve one user's filtered results to another, or serve stale post-merge duplicates — same trust-bug class as L1627. The item explicitly forbids shipping this without an invalidation decision; the short-TTL first cut IS that decision, made by the item's own text, not invented here.
