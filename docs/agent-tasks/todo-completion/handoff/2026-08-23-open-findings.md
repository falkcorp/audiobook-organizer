<!-- file: docs/agent-tasks/todo-completion/handoff/2026-08-23-open-findings.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9c0a71-5b28-4d6e-9a13-7c40e8b2d561 -->
<!-- last-edited: 2026-08-23 -->

# Open findings — TODO-completion package, 2026-08-23

Blocking and near-blocking findings that outlived the session that found them.
Every claim here was verified by running something; where a claim is inferred
rather than run, it says so.

## 1. BLOCKING, and it affects ALREADY-MERGED code

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

## 2. BLOCKING (PR #2787) — a live writer creates rows memdb structurally cannot hold

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

## 3. HIGH (PR #2787) — hand-written upper-bound sentinel the repo condemns in writing

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

## 4. IMPORTANT (PR #2787) — the abort message's only actionable field is always empty

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

## 6. TASK-084's brief is INVALID as written

Its goal is adding `// lgtm[go/disabled-certificate-check]` comments. **`lgtm[]`
suppression is inert in this repo** — it is the legacy LGTM.com mechanism that
GitHub code scanning never adopted.

Proven empirically on PR #2781: the `lgtm[]` markers were REMOVED and the
comments rewritten, and all four alerts (#1477/#1478/#1429/#1105) stayed open
across the merge. Only a code-scanning API dismissal closed #1429 and #1105.

Running 084 as briefed would ship three inert comments and mark the findings
"handled" while they stayed open — worse than leaving it alone. Rewrite it to
dismiss via the API, or to fix the code.

## 7. TASK-083 is partially done, not done

PR #2781 merged, but classified four findings and closed only two.
`#1477`/`#1478` (`internal/fileops/safe_operations.go`) are **real**, not false
positives: `op.backupPath` is built by
`safepath.Join(filepath.Dir(targetPath), …)`, so the containment root derives
from the taint. Worked example: `targetPath = "foo/../../../etc/passwd"` →
`Dir` = `"../../etc"` → `Join+Clean` = `"../../etc/.audiobook-backups"` → the
prefix check PASSES. Needs a real fix, not a classification.

Also outside 083's scope and still open: `#1603`
(`internal/fileops/hash.go`), `#1543` (`internal/metafetch/service.go`).
