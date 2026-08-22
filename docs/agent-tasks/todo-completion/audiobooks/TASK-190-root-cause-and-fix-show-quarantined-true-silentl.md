<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-190-root-cause-and-fix-show-quarantined-true-silentl.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8322dd9f-d3e9-4155-b46f-a5d2fcd51c92 -->
<!-- last-edited: 2026-08-21 -->

# TASK-190 — Root-cause and fix: show_quarantined=true silently narrows the audiobook list to is_primary_version=true (TODO.md L3718)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · audiobooks subagent · **Why:** the obvious candidate code paths were read in full and do NOT reproduce the reported divergence, so this needs careful bisection across multiple layers (service_query.go pushdown selection, buildBookSummaryFilter, the search/Bleve path, and the list-cache warmer in library_list_warmer.go which pre-populates cache entries keyed on IsPrimaryVersion=true) rather than a known fix applied mechanically · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 3718 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`show_quarantined=true` SHRINKS the list.** A fl" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-190-root-cause-and-fix-show-quarantined-true-silentl" -b agent/audiobooks-190-root-cause-and-fix-show-quarantined-true-silentl origin/main
cd "$REPO/.worktrees/audiobooks-190-root-cause-and-fix-show-quarantined-true-silentl"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Reproduce the reported divergence with a deterministic, fixture-seeded Go test (not a live prod query) that creates a small set of books spanning is_primary_version={true,false,nil} crossed with quarantined={true,false}, then calls the audiobook list/count path twice -- once with the default filters (ExcludeQuarantined=true, IsPrimaryVersion=nil) and once with ExcludeQuarantined=false, IsPrimaryVersion=nil (i.e. show_quarantined=true, no explicit primary filter) -- and asserts both calls return the SAME set of nil-flag and false-flag books (only differing in whether quarantined rows are included). Once the test fails and pinpoints the actual code path responsible (candidates already ruled out by static reading: the hasPostFilters branch selection and the memdb index-selection switch in both GetBookSummaries and CountBookSummaries -- the bug is NOT there), bisect outward to internal/server/library_list_warmer.go's cache pre-warming (it pre-populates svc.listCache with entries keyed IsPrimaryVersion=&primaryTrue for many limit/offset/sort combinations -- confirm the cache key in service_query.go:195-196 cannot collide between a warmed true-primary entry and an incoming nil-primary request) and to the Bleve search-index path if search-index-backed counts are involved, and fix whichever layer is actually responsible.

## Background (verify before editing)

- Measured on production 2026-08-14: the default query returned 63,869 books; the SAME query with only show_quarantined=true added returned 41,319 = 41,317 (the is_primary_version=true population) + 2 quarantined rows - adding a filter that can only WIDEN the set instead narrowed it to primary-only.
- RULED OUT before you start, do not re-investigate: a listCache key collision with library_list_warmer.go's warmed IsPrimaryVersion=&primaryTrue entries. internal/audiobooks/service_query.go:183-189 renders IsPrimaryVersion as the literal token 'nil'/'true'/'false' into primaryKey, and service_query.go:195-196 includes both primaryKey and noq=<ExcludeQuarantined> in the key, so a nil-primary request can never address a primary=true warmed page.
- ALSO RULED OUT by static reading: hasHeavyPostFilters/hasPostFilters (internal/audiobooks/service_query.go:77-78) do not key on ExcludeQuarantined, and the memdb index-selection switches (internal/database/memdb_summaries.go:132 and :281) default to the unrestricted memIdxID whenever IsPrimaryVersion is nil, independent of ExcludeQuarantined.
- Since every in-repo candidate is ruled out, the first deliverable is the fixture reproduction itself. If it does not reproduce at fixture scale, the divergence is data- or index-shaped (Bleve/search-backed counts, or the prod store) and this brief's scope is wrong - report that instead of forcing a fix.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'hasHeavyPostFilters\|hasPostFilters' internal/audiobooks/service_query.go   # declared at L77-78 as a function only of LibraryState/Tag/Tags/FieldFilters/PerUser/heavySorting/FingerprintingFilters/IsPrimaryVersion/sortPushdownable -- ExcludeQuarantined is not one of the inputs — hasHeavyPostFilters/hasPostFilters (the switch that picks the light vs heavy pushdown path) do not key on ExcludeQuarantined -- both show_quarantined states take the identical code branch given no other filters
  grep -n 'case f.IsPrimaryVersion != nil:' -A2 internal/database/memdb_summaries.go   # 1 hit at L132-135 (GetBookSummaries): default branch (line 134-135) is 'iter, err = txn.Get(memTableBooks, memIdxID)' with no ExcludeQuarantined involvement in index selection — the memdb list path (GetBookSummaries) selects the unrestricted ID index whenever IsPrimaryVersion is nil, independent of ExcludeQuarantined
  grep -n 'case f.IsPrimaryVersion != nil:' -A2 internal/database/memdb_summaries.go   # 2nd hit at L281-284 (CountBookSummaries), same default-to-memIdxID pattern — the memdb count path has the identical nil-IsPrimaryVersion default behavior
  git show 46628240:TODO.md | sed -n '3703,3721p'   # shows 'Follow-up bugs found by the controls (route to C1/C3, do NOT fix here):' immediately above the show_quarantined bullet — the bug was measured against production and explicitly deferred rather than fixed inline
  ```

### Reuse — don't invent

- Use `BookSummaryFilter (shared filter struct across memdb/Pebble)` in `internal/database/memdb_summaries.go` (verify: `grep -n 'type BookSummaryFilter struct' internal/database/memdb_summaries.go`) — do NOT write a parallel helper.
- Use `existing production-measured-bug-as-test pattern (fixture-seeded oracle vs prod counts)` in `internal/server/handlers/abs/series_books_test.go` (verify: `grep -n 'func absSeedTwoSeries' internal/server/handlers/abs/series_books_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add a new test file internal/audiobooks/service_query_primary_quarantine_test.go. Seed a MemStore (or the existing test double used elsewhere in this package -- grep for the fixture-store constructor already used by other service_query_test.go-style tests in this package) with 4 books: one IsPrimaryVersion=true not quarantined, one IsPrimaryVersion=false not quarantined, one IsPrimaryVersion=nil not quarantined, one IsPrimaryVersion=nil quarantined.
2. Call svc.GetAudiobooksWithTotal(ctx, 0, 0, "", nil, nil, ListFilters{ExcludeQuarantined: true}) (simulating the default request) and assert it returns exactly the 3 non-quarantined books (true/false/nil primary all included, since IsPrimaryVersion is unset).
3. Call svc.GetAudiobooksWithTotal(ctx, 0, 0, "", nil, nil, ListFilters{ExcludeQuarantined: false}) (simulating show_quarantined=true) and assert it returns all 4 books. If it instead returns only the IsPrimaryVersion=true book (reproducing the reported bug), the test now fails deterministically and is the reproduction.
4. With the failing test in hand, add temporary slog.Debug or t.Logf tracing at each candidate decision point already identified as innocent by static reading (service_query.go's hasPostFilters computation, the pushdown branch selection, memdb_summaries.go's index-selection switch) to CONFIRM they are in fact taking the same branch in both calls -- do not assume the earlier static read was complete; verify it under the failing test.
5. If both calls confirm identical branch selection through memdb_summaries.go, extend the trace into internal/server/audiobooks_helpers.go's buildAudiobookListResponse (the safety-net quarantine re-filter at line ~69-77) and into whatever populates svc.listCache -- specifically check whether library_list_warmer.go's pre-warmed entries (IsPrimaryVersion=&primaryTrue) can be reached by a cache-key collision from the nil-IsPrimaryVersion request under test.
6. Once the actual divergent code path is found, apply the minimal correct fix at that location (do not paper over it by special-casing ExcludeQuarantined in a branch condition without understanding why the divergence occurred there) and confirm the new test (and the old default-request test) both pass.
7. Re-run the existing fixture-based tests in internal/audiobooks/... and internal/database/... to confirm no other pushdown/count test regresses.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_190.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the root cause turns out to be a listCache key collision from library_list_warmer.go's pre-warmed IsPrimaryVersion=true entries, the fix must not simply stop warming those entries (they exist for a real performance reason -- prod 'library spins forever' history cited at service_query.go:75) -- the cache KEY must be made collision-proof instead.
- If IsPrimaryVersion=false (22,552, per todo_line 3729) turns out to currently be an empty population in prod (a possibility that item explicitly raises), the reproduction test's false-flag book must still be included in the fixture and asserted correctly, independent of what todo_line 3729's prod measurement finds -- the code-level correctness question and the prod-data-population question are separate.

## Tests

- internal/audiobooks/service_query_primary_quarantine_test.go TestGetAudiobooksWithTotal_ShowQuarantinedDoesNotNarrowByPrimary -- the reproduction test described in steps above; must fail before the fix and pass after.
- internal/audiobooks/service_query_primary_quarantine_test.go TestCountAudiobooksFiltered_ShowQuarantinedDoesNotNarrowByPrimary -- same scenario against CountAudiobooksFiltered, since the reported prod numbers (63,869 vs 41,319) came from full page-through counts, not a single page.
- Full package regression: go test ./internal/audiobooks/... ./internal/database/... -count=1

Anti-over-suppression test: `TestGetAudiobooksWithTotal_ShowQuarantinedDoesNotNarrowByPrimary itself is the anti-suppression check: it specifically asserts the WIDER filter set (show_quarantined=true) never returns FEWER books than a narrower one on the same request shape` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] T
- [ ] h
- [ ] e
- [ ]  
- [ ] r
- [ ] e
- [ ] p
- [ ] r
- [ ] o
- [ ] d
- [ ] u
- [ ] c
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] m
- [ ] u
- [ ] s
- [ ] t
- [ ]  
- [ ] F
- [ ] A
- [ ] I
- [ ] L
- [ ]  
- [ ] b
- [ ] e
- [ ] f
- [ ] o
- [ ] r
- [ ] e
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] a
- [ ] n
- [ ] d
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ]  
- [ ] a
- [ ] f
- [ ] t
- [ ] e
- [ ] r
- [ ] :
- [ ]  
- [ ] r
- [ ] e
- [ ] c
- [ ] o
- [ ] r
- [ ] d
- [ ]  
- [ ] `
- [ ] g
- [ ] o
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] .
- [ ] /
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] a
- [ ] u
- [ ] d
- [ ] i
- [ ] o
- [ ] b
- [ ] o
- [ ] o
- [ ] k
- [ ] s
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] -
- [ ] r
- [ ] u
- [ ] n
- [ ]  
- [ ] S
- [ ] h
- [ ] o
- [ ] w
- [ ] Q
- [ ] u
- [ ] a
- [ ] r
- [ ] a
- [ ] n
- [ ] t
- [ ] i
- [ ] n
- [ ] e
- [ ] d
- [ ] D
- [ ] o
- [ ] e
- [ ] s
- [ ] N
- [ ] o
- [ ] t
- [ ] N
- [ ] a
- [ ] r
- [ ] r
- [ ] o
- [ ] w
- [ ] B
- [ ] y
- [ ] P
- [ ] r
- [ ] i
- [ ] m
- [ ] a
- [ ] r
- [ ] y
- [ ]  
- [ ] -
- [ ] c
- [ ] o
- [ ] u
- [ ] n
- [ ] t
- [ ] =
- [ ] 1
- [ ] `
- [ ]  
- [ ] f
- [ ] a
- [ ] i
- [ ] l
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] p
- [ ] a
- [ ] r
- [ ] e
- [ ] n
- [ ] t
- [ ]  
- [ ] c
- [ ] o
- [ ] m
- [ ] m
- [ ] i
- [ ] t
- [ ]  
- [ ] a
- [ ] n
- [ ] d
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] ,
- [ ]  
- [ ] a
- [ ] n
- [ ] d
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] t
- [ ] e
- [ ]  
- [ ] b
- [ ] o
- [ ] t
- [ ] h
- [ ]  
- [ ] o
- [ ] u
- [ ] t
- [ ] p
- [ ] u
- [ ] t
- [ ] s
- [ ]  
- [ ] i
- [ ] n
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] r
- [ ] e
- [ ] p
- [ ] o
- [ ] r
- [ ] t
- [ ] .
- [ ]  
- [ ] I
- [ ] f
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] f
- [ ] i
- [ ] x
- [ ] t
- [ ] u
- [ ] r
- [ ] e
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] p
- [ ] a
- [ ] r
- [ ] e
- [ ] n
- [ ] t
- [ ]  
- [ ] c
- [ ] o
- [ ] m
- [ ] m
- [ ] i
- [ ] t
- [ ] ,
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] b
- [ ] u
- [ ] g
- [ ]  
- [ ] d
- [ ] o
- [ ] e
- [ ] s
- [ ]  
- [ ] N
- [ ] O
- [ ] T
- [ ]  
- [ ] r
- [ ] e
- [ ] p
- [ ] r
- [ ] o
- [ ] d
- [ ] u
- [ ] c
- [ ] e
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] f
- [ ] i
- [ ] x
- [ ] t
- [ ] u
- [ ] r
- [ ] e
- [ ]  
- [ ] s
- [ ] c
- [ ] a
- [ ] l
- [ ] e
- [ ]  
- [ ] -
- [ ]  
- [ ] S
- [ ] T
- [ ] O
- [ ] P
- [ ]  
- [ ] a
- [ ] n
- [ ] d
- [ ]  
- [ ] r
- [ ] e
- [ ] p
- [ ] o
- [ ] r
- [ ] t
- [ ]  
- [ ] t
- [ ] h
- [ ] a
- [ ] t
- [ ]  
- [ ] a
- [ ] s
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] f
- [ ] i
- [ ] n
- [ ] d
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] r
- [ ] a
- [ ] t
- [ ] h
- [ ] e
- [ ] r
- [ ]  
- [ ] t
- [ ] h
- [ ] a
- [ ] n
- [ ]  
- [ ] c
- [ ] o
- [ ] m
- [ ] m
- [ ] i
- [ ] t
- [ ] t
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] a
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] e
- [ ] n
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ] ;
- [ ]  
- [ ] d
- [ ] o
- [ ]  
- [ ] n
- [ ] o
- [ ] t
- [ ]  
- [ ] p
- [ ] r
- [ ] o
- [ ] c
- [ ] e
- [ ] e
- [ ] d
- [ ]  
- [ ] t
- [ ] o
- [ ]  
- [ ] a
- [ ]  
- [ ] f
- [ ] i
- [ ] x
- [ ] .
- [ ]  
- [ ] g
- [ ] o
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] .
- [ ] /
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] a
- [ ] u
- [ ] d
- [ ] i
- [ ] o
- [ ] b
- [ ] o
- [ ] o
- [ ] k
- [ ] s
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] .
- [ ] /
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] d
- [ ] a
- [ ] t
- [ ] a
- [ ] b
- [ ] a
- [ ] s
- [ ] e
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] -
- [ ] c
- [ ] o
- [ ] u
- [ ] n
- [ ] t
- [ ] =
- [ ] 1
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ]  
- [ ] w
- [ ] i
- [ ] t
- [ ] h
- [ ]  
- [ ] n
- [ ] o
- [ ]  
- [ ] n
- [ ] e
- [ ] w
- [ ]  
- [ ] f
- [ ] a
- [ ] i
- [ ] l
- [ ] u
- [ ] r
- [ ] e
- [ ] s
- [ ] .
- [ ] Anti-over-suppression test: `TestGetAudiobooksWithTotal_ShowQuarantinedDoesNotNarrowByPrimary itself is the anti-suppression check: it specifically asserts the WIDER filter set (show_quarantined=true) never returns FEWER books than a narrower one on the same request shape` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/audiobooks/... ./internal/database/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_190.md`.

## Commit message

```
fix(audiobooks): Root-cause and fix: show_quarantined=true silently narrows t (TODO L3718)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Companion to todo_line 3729, which asks a pure production-data question (is the false-flag population actually 0 in prod, or is the false-filter itself returning nils) -- that is prod_run, not code work, and does not block this item's reproduction-via-fixture approach. This item's root cause is NOT located despite reading service_query.go, service_filtering.go, and both memdb_summaries.go index-selection switches in full -- flagged opus-tier specifically because the obvious candidates were ruled out and a harder bisection is needed. review_critical=false: this is a read-path listing/count bug (wrong books/counts SHOWN), not a write path, but it does affect which books an operator believes exist, so treat any fix with normal review care even though it's not marked critical by the schema's write-path criterion.
