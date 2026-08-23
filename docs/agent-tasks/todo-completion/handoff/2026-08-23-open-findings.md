<!-- file: docs/agent-tasks/todo-completion/handoff/2026-08-23-open-findings.md -->
<!-- version: 1.4.0 -->
<!-- guid: 3f9c0a71-5b28-4d6e-9a13-7c40e8b2d561 -->
<!-- last-edited: 2026-08-23 -->

# Open findings — TODO-completion package, 2026-08-23

Blocking and near-blocking findings that outlived the session that found them.
Every claim here was verified by running something; where a claim is inferred
rather than run, it says so.

## 1. RESOLVED 2026-08-23 — both halves shipped

**Series half: PR #2794 (merged). Author half: PR #2787, commit `81612d4b3`.**

`MemStore` now refuses via `requireTablesComplete` (`memdb_integrity.go`) and
`PebbleStore` falls through to the authoritative Pebble scan on
`ErrMemdbIncomplete`, so the answer is correct rather than merely safe.

Three things the original finding did not anticipate, all now settled:

- **The suggested fix would not have worked.** It proposed keying off warmup's
  existing `skips` map, but that map is only incremented by `safeInsert`; a
  `json.Unmarshal` failure returned `(false, nil)` and was never tallied — and
  the finding's own repro (corrupt a value) goes down exactly that untracked
  path. Both loss sites now funnel through one `lose()` helper.
- **Warmup is not the only way a row goes missing.** `applyMemSync` aborting at
  runtime leaves the identical gap with no restart involved, and that is the one
  that keeps the guard fail-open in steady state. Recorded against
  `memTableUnknown`, which taints every table because the failing closure is
  opaque.
- **The author guard must name BOTH tables it scans**, not just `books` the way
  the series twin does. A lost `book_authors` row is a co-author credit that
  exists nowhere else. Mutation-tested: naming only `memTableBooks` compiles,
  reads as consistent with its sibling, and still fails open.

*Original finding follows.*

**The unfiltered ref-count guards are hardened on a code path production never
takes.**

`PebbleStore.GetAllSeriesBookRefCounts` (`internal/database/series_bookref.go`)
and `PebbleStore.GetAllAuthorBookRefCounts`
(`internal/database/author_bookref.go:158`) both dispatch like this:

```go
if p.UseMemDB && p.mem() != nil {
    return p.mem().GetAll...RefCounts()
}
return p.getAll...RefCountsPebble()
```

`UseMemDB` is hardcoded `true` at `internal/database/pebble_store.go:319`
("in-memory query layer is the default after Phase 3"). So the `iter.Error()`
checks and the "undecodable row is FATAL" returns added by **PR #2782 (merged,
series)** and proposed by **PR #2787 (open, authors)** never execute in
production.

The memdb branch has no equivalent guard, because the rows were already dropped
upstream at warmup. `internal/database/memdb_warmup.go:247` does:

```go
if err := json.Unmarshal(val, &list); err != nil {
    return false, nil        // undecodable row: SKIPPED, warmup still succeeds
}
```

and `safeInsert` (`memdb_warmup.go:147-161`) logs at Warn, mutes after 10 per
table, and returns `(false, nil)`. `memPtr` is published regardless.

**Confirmed by probe**, not by reading. Write a 2-author credit list, corrupt
that `book_authors:<bookID>` value in Pebble, close, reopen, `WaitForWarmup`:

```
after restart, interface method -> counts=map[2:1] err=<nil>
FAIL-OPEN: author 3 count = 0 (present=false)
```

The author existed only in that junction row. The guard says "referenced by
nothing", the error is nil, and `maintenance.purge-empty-authors` — 4,975 of
12,854 authors eligible on the live library — deletes it.

Bounded, not unbounded: `memPtr.Store(m)` happens once after full warmup
(`internal/database/memdb_pending.go:174,217`), so `mem()` is nil until
publication and pre-publish reads DO fall through to the hardened Pebble scan.
There is no boot window where the guard is off. Exposure is warmup row-skips
only, observable on the live library via `skipped_total` in the
"memdb warmup complete" log line.

**Consequences:**
- `changelog.d/20260821_database_028.md` is materially wrong where it says
  "Both now abort the scan". On the production path a short count still returns
  with a nil error.
- The already-merged series twin needs the same fix. #2782 closed a real bug on
  the cold path and left the warm path fail-open.
- Suggested fix: warmup already tracks `skips[table]`; persist it on the
  `MemStore` and have the memdb counters return an error when
  `skips[memTableBooks] > 0 || skips[memTableBookAuthors] > 0`, or have the
  `PebbleStore` method fall through to the Pebble scan in that case. Fail
  closed, the way the Pebble scan now does.

## 2. RESOLVED 2026-08-23 (PR #2787, `cc81a92ec`) — a live writer created rows memdb structurally cannot hold

Fixed at the call site (`handler.go` author-split now sets `BookID`) AND by a
backfill in `ReplaceBookAuthorsInMemDB`, so the next caller cannot re-arm it.
Regression test asserts memdb and Pebble AGREE rather than merely that no error
was returned — the bug never produced an error. Mutation-tested both ways:
neutralizing the backfill and making it overwrite instead of fill fail different
tests.

*Original finding follows.*

`internal/server/handlers/operations/handler.go:273-276` (the author-split op)
appends `database.BookAuthor{AuthorID: a.ID, Role: "author"}` and **never sets
`BookID`**. It is the only one of 13 non-test `SetBookAuthors` call sites that
omits it.

Chain, verified: `SetBookAuthors` marshals the slice verbatim into
`book_authors:<bookID>`, so Pebble holds `[{"book_id":"",...}]`. It then calls
`ReplaceBookAuthorsInMemDB`, whose primary index
(`memdb_schema.go:313-327`) is a non-`AllowMissing` compound index on
`{BookID, AuthorID}`. go-memdb returns "object missing primary index" for the
empty string, `applyMemSync` aborts the txn and logs
`memdb sync failed (pebble still authoritative)`, and `SetBookAuthors` still
returns nil. A restart does not repair it — warmup hits the same rule.

Result: Pebble holds the credits, memdb holds neither, and BOTH the filtered
counter and the new unfiltered guard report 0. The split authors are bulk-deleted
and the book keeps two `author_id`s that no longer resolve.

Fix needs both halves: set `BookID: book.ID` at the call site, AND have
`ReplaceBookAuthorsInMemDB` backfill `a.BookID = bookID` (it already has the
argument) so no future caller can re-arm the trap.

## 3. RESOLVED 2026-08-23 (PR #2787, `cc81a92ec`) — hand-written upper-bound sentinel

Now `prefixUpperBound(jPrefix)`. Book IDs are caller-supplied, so the literal
`~` (0x7E) excluded every non-ASCII ID's whole credit list.

*Original finding follows.*

`internal/database/author_bookref.go:175-176` uses
`UpperBound: []byte("book_authors:~")`. `internal/database/pebble_store_authors.go:221-227`
explicitly warns against exactly this for exactly this keyspace: bookID is an
opaque string, and a literal `~` (0x7E) excludes any id whose first byte is
higher, which includes every non-ASCII id (UTF-8 continuation bytes start at
0xC2). `CreateBook` only generates a ULID `if book.ID == ""`, so importers and
restore paths can supply their own.

`prefixUpperBound` already exists in the package (`embedding_store.go:1710`).
Incidence on the live library was NOT measured — the mechanism is confirmed, the
occurrence rate is unknown.

## 4. RESOLVED 2026-08-23 (PR #2787, `cc81a92ec`) — the abort message's key field was always empty

`badKey` is now captured BEFORE `jIter.Close()`.

*Original finding follows.*

`internal/database/author_bookref.go:189-190` calls `jIter.Close()` and then
`jIter.Key()` in the very next statement's format args. Probe output:
`undecodable book_authors row ""`. Capture the key before closing. The book pass
at `:238` already does this correctly; only the junction pass is affected.

## 5. Pre-existing author-delete family, never guarded

All of these enumerate books through the FILTERED
`GetBooksByAuthorIDWithRoleCore`, `continue` past per-book failures, and call
`DeleteAuthor` anyway:

- `internal/plugins/maintenance/author.go:250` — the H8 comment at `:121-127`
  *describes the stranding* and ships it anyway, adding only a counter.
- `internal/scheduler/extra_ops.go:414`
- `internal/server/handlers/entities/handler.go:517` (SplitAuthor), `:825`
  (ConvertAuthorToNarrator)
- `internal/plugins/maintenance/author_conjunction_repair.go:372`
- `internal/server/entities_ops.go:159` — partly mitigated by
  `CreateAuthorTombstone` right after.

## 6. RESOLVED 2026-08-23 — TASK-084's brief was INVALID as written (now rewritten)

> **Status: the four affected briefs were rewritten on 2026-08-23** (TASK-084 →
> v2.0.0, TASK-026 → v2.0.0, TASK-080 → v2.0.0, TASK-083 → v2.0.0). Each now
> carries a banner with the evidence and directs the agent to a code-scanning
> API dismissal instead. They are safe to dispatch.
>
> **Stronger evidence found while rewriting.** The proof below is "markers
> removed, alerts stayed open". There is a cleaner one: alert **#1104** is
> `open` at `internal/audiobooks/service_mutation.go:63`, and that exact line
> carries `// lgtm[go/path-injection]` **today**. Marker present, alert open —
> no inference about the merge required:
> ```bash
> grep -n 'lgtm\[' internal/audiobooks/service_mutation.go
> gh api /repos/falkcorp/audiobook-organizer/code-scanning/alerts/1104 -q '.state'
> ```
> Two of the three `lgtm[go/path-injection]` markers in the tree
> (`internal/importer/collision.go`, `internal/server/bootstrap.go`) have no
> corresponding open alert, but that is NOT evidence they worked — those lines
> may simply never have been flagged. Only the service_mutation.go case is
> decisive, because the marker and the open alert share a `path:line`.
>
> **A fourth false claim, found in TASK-083 and not recorded below:** that brief
> cited `service_mutation.go:63` as "already suppressed" and told the agent to
> copy it as the template. It is the counter-example, not the template.

## 6a. Original finding (kept for the record)

TASK-084's brief is INVALID as written

Its goal is adding `// lgtm[go/disabled-certificate-check]` comments. **`lgtm[]`
suppression is inert in this repo** — it is the legacy LGTM.com mechanism that
GitHub code scanning never adopted.

Proven empirically on PR #2781: the `lgtm[]` markers were REMOVED and the
comments rewritten, and all four alerts (#1477/#1478/#1429/#1105) stayed open
across the merge. Only a code-scanning API dismissal closed #1429 and #1105.

Running 084 as briefed would ship three inert comments and mark the findings
"handled" while they stayed open — worse than leaving it alone. Rewrite it to
dismiss via the API, or to fix the code.

**Two more briefs rest on the same false premise**, found by a keyword sweep:
**TASK-026** (part 1 of 3) and **TASK-080** (conditionally, at step 3). Rewrite
those the same way. The sweep was widened beyond "lgtm" and found no others
(one false positive: TASK-189 matched "nosec" inside "nanoseconds").

## 7. TASK-083 is partially done, not done — BRIEF REWRITTEN 2026-08-23

> **Status: rewritten to v2.0.0.** Scope is now #1477/#1478 only, with #1429 and
> #1105 marked done and explicitly out of scope. The brief now states the fix
> (re-root containment at a trusted base independent of `targetPath`, validate
> before deriving, resolve symlinks first), forbids dismissing them, and
> requires a run-and-pasted negative control plus a positive control.
>
> Corroborated independently: `internal/fileops/safe_operations.go` already
> carries an accurate in-code comment (added by #2781) stating the alert is not
> dismissed, that the containment argument was wrong, and "Left open
> deliberately; see TASK-083". It also records that neither upstream gate
> (`fileops.ValidateUserPath`, `IsAllowedPath`) resolves symlinks — that gap is
> part of the fix.

## 7a. Original finding (kept for the record)

TASK-083 is partially done, not done

PR #2781 merged, but classified four findings and closed only two.
`#1477`/`#1478` (`internal/fileops/safe_operations.go`) are **real**, not false
positives: `op.backupPath` is built by
`safepath.Join(filepath.Dir(targetPath), …)`, so the containment root derives
from the taint. Worked example: `targetPath = "foo/../../../etc/passwd"` →
`Dir` = `"../../etc"` → `Join+Clean` = `"../../etc/.audiobook-backups"` → the
prefix check PASSES. Needs a real fix, not a classification.

Also outside 083's scope and still open: `#1603`
(`internal/fileops/hash.go`), `#1543` (`internal/metafetch/service.go`).

## 8. From the #2787 review sweep, 2026-08-23 — 3 OPEN, deliberately deferred

Three review agents (go-specialist, silent-failure-hunter, comment-analyzer) ran
on #2787's diff. Five findings were fixed in-PR; three were filed as `todo.d`
fragments rather than folded in, to keep an owner-gated prod-data PR reviewable.
Those three are recorded here because a `todo.d` fragment is invisible until the
daily collector folds it into `TODO.md`.

### 8.1 OPEN — the bulk purge's headline safety is itself a filtered counter

`author_purge_empty.go` labels `require_zero_files` **"🔴 THIS IS THE SAFETY THAT
MATTERS"** and defaults it ON, to protect the 822 authors whose zero-book count
looks more like a broken link than an empty author. It reads
`GetAllAuthorFileCounts`, and BOTH implementations (`memdb_reads.go`,
`pebble_store_authors.go`) scan only the primary-version index, skip
soft-deleted books, and map books to authors through the legacy `Book.AuthorID`
field only — never the junction.

So `fileCounts[id]` is **unconditionally 0** for a junction-only co-author, and
for any legacy author whose books are all trashed or all non-primary. Those are
precisely the three populations the ref guard exists for. **The backup safety
cannot hold back a single case the primary guard was built to catch.**

The same function still carries the three defects #2787 fixed in the ref scan:
undecodable book row silently skipped, `iter.Error()` never checked, and a
swallowed `GetBookFilesForIDsCore` error.

Structural point worth keeping: the candidate selector, the ref gate and the
file safety all read the SAME lossy memdb. They are correlated, not independent
— which is why a loss site that never sets the `lostRows` flag (8.4) defeats all
three at once.

### 8.2 OPEN — seven unlogged error drops in `memdb_sync.go`

Seven delete helpers do `if err != nil || obj == nil { continue }` around a
`txn.First`, so a real lookup failure is indistinguishable from an absent row and
is neither logged nor recorded. All fail **closed** for the ref counters (memdb
retains a row Pebble deleted → over-count), which is why they are not urgent —
but they are seven silent error drops in the one file that owns the memdb/Pebble
invariant. Related: `loadBookFilesForBookID` drops undecodable rows and returns
nil, and `UpsertBookToMemDB` uses that short list to REPLACE memdb's `book_files`
for the book.

### 8.3 OPEN — `book:0`..`book:;` needs a MEASUREMENT, not a guess

~20 sites scan book records with that hand-written range, admitting only `'0'`–
`'9'` and `':'` as the first byte after the colon. Every minting site produces a
ULID (leading `0`–`7`), so it holds today — but `CreateBook` only mints when
`book.ID == ""`, so importers and restore paths supply their own, and
`pebble_store.go` describes the same keyspace as "below any UUID character
(0-9, a-f, '-')", which a UUID-leading id falls outside in BOTH directions.

Deliberately left unmeasured rather than graded: it needs a live prefix scan for
`book:` keys whose first byte after the colon is not a digit. **Do not "fix" it
without measuring** — widening is safe, but the narrow range is the package-wide
convention and changing ~20 sites on a guess is not.

### 8.4 FIXED, recorded because the next such bug will look like it

`UpsertBookToMemDB` lost rows on the **committed** path: it cleared a book's
`book_authors`/`book_narrators`/`book_files` and skipped the reinsert when the
Pebble read had errored, committing an empty set while memdb reported itself
complete. The other three loss sites all end in an ABORTED transaction, which is
how `applyMemSync` catches them centrally — so `recordLostRows` never fired and
`requireTablesComplete` returned nil.

It was strictly MORE permissive than the path #2782 hardened: `GetBookAuthors`
errors on an undecodable credit list, and on that identical row the Pebble ref
scan is fatal.

**The generalizable lesson, now in `memdb_integrity.go`: a loss detector that
hooks the failure path cannot see a loss that travels the success path.** That
file's confident "there are THREE" was itself the camouflage — it read as an
exhaustive audit, so nothing looked for a fourth shape. The count now says FOUR,
is dated, and asks the next editor to update it.

### 8.5 FIXED — a later commit defused an earlier test, in the same PR

The §2 regression test was mutation-verified as discriminating when written. The
§1 fall-through then defused it: with the backfill reverted, the bad insert is
rejected → the store is flagged → the call falls through to Pebble → the correct
answer comes back → **the test passes against broken code.** Confirmed by
re-running the mutation at HEAD, not by argument.

Re-armed with `require.Empty(t, store.mem().LostRows())`. **Re-run mutation
matrices at final HEAD, not only when each test is authored** — a fix that makes
the system more robust can make a test less discriminating.
