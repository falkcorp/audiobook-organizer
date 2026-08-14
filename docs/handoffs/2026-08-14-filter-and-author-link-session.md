<!-- file: docs/handoffs/2026-08-14-filter-and-author-link-session.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f7c2b91-8d35-4e60-a1f9-6b208c53d7ea -->
<!-- last-edited: 2026-08-14 -->

# Handoff — 2026-08-14 — filter guards, author links, and what is still open

Written for a follow-on session picking up every outstanding thread. Everything
below is either **verified** (measured, with the measurement shown) or
explicitly marked **UNVERIFIED**. Please keep that distinction — several items
here look settled and are not.

---

## 1. Shipped and deployed

Three dual-implementation (memdb vs Pebble) divergences, merged **and** live in
production. The deployed binary is from **07:51:49**, restarted 07:52:57, memdb
warm at 07:55:07 (129s warmup).

| PR | what diverged |
|---|---|
| #2406 | `GetBooksByAuthorIDCore` — Pebble scanned only the legacy `AuthorID` column and was blind to co-authors; memdb did junction+legacy minus non-primary |
| #2410 | `GetBooksByAuthorIDWithRoleCore` — Pebble included non-primary versions, memdb excluded them (opposite sign to the above, which is why aggregate counts looked plausible) |
| #2411 | `GetBooksBySeriesIDCore` primary-version filter + ordering; `GetAllSeriesBookCounts` / `GetAllSeriesFileCounts` / `CountBooksByPathPrefix` soft-delete filtering |
| #2413 | `dbtest.AssertStoreInvariants` — invariant (a) could never fire because `ListBookIDs` omits soft-deleted books; now unions `ListSoftDeletedBooks` |
| #2416 | docs: 56 duplicate-name author groups, split by kind |

**Semantics chosen** (documented so it is not re-litigated): `...WithRoleCore`
returns the COMPLETE set — merges and deletes use it, and a missed link there is
data loss. `...Core` is the LISTING view (primary-only) — it matches what memdb
already served warm, so only the cold path changed, and it changed to match warm.

**Production confirmation of the fix:** the same dry run that reported `books=0`
on the warm path in the morning reported `books=2, books_relinked=2` after
deploy. That is the real evidence the divergence closed — not the test suite.

---

## 2. PR #2417 — OPEN, needs merge

Branch `fix/bare-filter-param-set-derived`, worktree
`../audiobook-organizer-bareparam`. **CI was 13 pass / 6 pending / 3 skipping
when last checked — verify and merge.**

### What it fixes

`ab04824e` (2026-08-13) started rejecting filter-field names passed as bare
query parameters, because gin ignores them and the request then lists the whole
library while looking exactly like a narrowed query. That guard was correct but
consulted a **hand-written map in the handler package — a third copy** of a list
that already had a single source of truth (`audiobooks.KnownFilterFields()`,
pinned to the matcher by `TestFilterFieldNames_MatchTheMatcher`). It had drifted
by 17 names.

Measured on production 2026-08-14 **with an unfiltered baseline**, which is what
makes it a comparison rather than an impression:

    ?year=2001                 -> count=63869
    ?work_id=abc               -> count=63869
    ?marked_for_deletion=true  -> count=63869
    (no filter, baseline)      -> count=63869

Drifted names: `year`, `work_id`, `isbn10`, `isbn13`, `series_number`,
`created_at`, `updated_at`, `marked_for_deletion`, `duration`, `bitrate`,
`bitrate_kbps`, `file_size`, `file_size_bytes`, `sample_rate`, `sample_rate_hz`,
`channels`, `bit_depth`.

The guard is now **derived** from `KnownFilterFields()` minus a two-name
allow-list, so the third copy no longer exists.

### The collision survey — read this before touching the allow-list

The governing asymmetry: omitting a name is harmless (the guard does not fire,
which is pre-guard behavior); **including one wrongly rejects a request that
used to work.**

My first survey repeated the exact mistake `ab04824e` warns about — it matched
`ParseQueryString("` and so could not see `ParseQueryString(c, "library_state")`.
The corrected pass returned `limit` but neither `offset` nor `page`, which is
what revealed it was *still* scoped too narrowly. Widened to all of
`internal/server` + `internal/httputil`: **95 param names, 44 canonical fields,
5 collisions.**

| name | accessor | same endpoint as the guard? |
|---|---|---|
| `library_state` | `handlers/audiobooks/handler.go:516` | **YES — the only real one** |
| `title` | `handlers/metadata/handler.go:389` | no |
| `author` | `handlers/metadata/handler.go:390` | no |
| `duration` | `server/audio_sample.go:39` | no |
| `format` | `handlers/dedup/handler.go:492` | no |

The guard has one call site and reads that request's own query string, so only
same-endpoint collisions matter. `title`/`author` have been guarded since
`ab04824e` with metadata search unaffected — empirical confirmation.

`fingerprint_status` is allow-listed although the derivation already excludes it
(it is not a canonical field), so the protection does not depend on that staying
true.

### Verification done

- 7 top-level tests, **81 subtests, 0 FAIL**, exit 0
- guard table exercises **43** names, up from 26 — counted explicitly, because a
  table that iterates nothing also reports success
- **negative control:** dropping one field from the derivation fails three
  separate tests and names it:
  `canonical filter field "year" is not guarded — a bare ?year= would list the entire library while looking like a narrowed query`
- `./internal/server/handlers/...` and `./internal/audiobooks/...` — green

### ⚠️ UNCOMMITTED work in that worktree

`todo.d/20260814-is-primary-version-nil-vs-false-divergence.md` was written but
**not committed** (the session was interrupted). Commit it before or with the
merge — it documents item 4 below.

---

## 3. `?version_group_id=` lists the whole library — filed, not fixed

    version_group_id='vg-08c1a396b'         -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA
    version_group_id='vg-TOTALLY-BOGUS-XYZ' -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA
    version_group_id=''                     -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA

A bogus ID and a real one are indistinguishable, so the parameter is not read.

**This is NOT the same bug as #2417 and #2417 does not fix it.** `?year=` was a
*known* field passed the wrong way. `version_group_id` is not a filter field at
all — it is absent from `allFilterFieldNames`, `bookFieldValue` has no case for
it, so the derived guard has nothing to match on.

Filed at `todo.d/20260814-version-group-id-not-a-filter-field.md` with both
options. The storage layer *does* index it (`memIdxVersionGroupID`,
`GetAllBooksFrom` accepts it as a filter key), so there is a real case for
promoting it to a filter field — that is matcher work plus a Pebble-path check.

---

## 4. `is_primary_version` means different things on two paths — NEW, filed

Found while trying to verify the author merge in item 5. Both paths accept the
parameter and answer confidently; they disagree about what a **nil** flag means.

Library-wide it partitions exactly, returning rows with an explicit `false`:

    is_primary_version=false -> total=22552
    is_primary_version=true  -> total=41317
    (no filter)              -> total=63869   = 22552 + 41317 ✓

On the author path it returns rows whose flag is **null**:

    author_id=38542&is_primary_version=false -> 1 row, is_primary_version: null
    author_id=38543&is_primary_version=false -> 1 row, is_primary_version: null

And it cannot return an explicitly-`false` book at all. Book
`01KNDB8NWHXV2DKRQESBA9SDRA` records `author_id: 42623`, `is_primary_version:
false`, yet:

    author_id=42623                          -> 1 row, NOT that book
    author_id=42623&is_primary_version=false -> 0 rows
    author_id=42623&is_primary_version=true  -> 1 row (a different book)

**Likely cause:** `memdb_schema.go` indexes with
`effectiveBoolFieldIndex{Default: true}`, so a nil `IsPrimaryVersion` indexes as
**true**; a post-filter comparing the raw `*bool` sees nil as "not true" and
calls it **false**. Same nil, opposite readings by layer.

Fragment: `todo.d/20260814-is-primary-version-nil-vs-false-divergence.md`
(**uncommitted — see item 2**).

---

## 5. Author 46627 merge — APPLIED, verification IMPOSSIBLE with current tools

The `& Author` conjunction repair. User approved applying it to author 46627.

**Verified:** author count 9,320 → 9,319; row 46627 gone; only **2** `&` names
remain and both are HTML-entity junk (`&#169`, `&#169;2013 by HarperCollins…`)
— no person-name rows left. Op reported `merged_into_existing:1`,
`books_relinked=2`, `rows_written=1`, no errors.

**UNVERIFIED:** that the two `BookAuthor` rows actually landed on 43791.

Do not treat the following as evidence of failure — I nearly did:

- `Nicholas Courtney` (43791) still shows **7** books, and
  `author_id=43791&is_primary_version=false` returns **0**.
- That is expected-looking, because the author listing is primary-only by
  design (#2410) AND, per item 4, cannot return explicitly-non-primary rows for
  *any* author. Author 42623 is the counterexample that proves the instrument is
  blind, not that 43791 is empty.
- The other instrument I reached for (`version_group_id`) is entirely ignored —
  item 3.

**To actually verify:** read the `book_authors:` rows directly from Pebble for
author 43791 and confirm the two book IDs are present. Note
`diagnostics query --raw` failed with "permission denied" on LOCK while the
service was running, so this needs either a service-aware path or a read against
a snapshot.

---

## 6. Other open findings, not yet filed as fragments

- **ABS series listing returns `numBooks > 0` with `books: []`** — 23 of 50 on
  page 0, one with `numBooks=110`. This is the surface behind the user's "shows
  zero series" complaint in AudioBooth. The native API is healthy
  (14,286/14,629 series, 14,300 with `book_count>0`), so the defect is in the
  ABS compatibility layer, not storage.
- **`/series/count` = 14,286 vs series list = 14,629** — a 343-row discrepancy,
  unexplained.
- **Junk series names** — `- 3`, `- Legion`, `- Freedom's Dawn`, and
  `-Dickens Short Stories` author rows. Same disease as the `& Name` rows: a
  metadata parse writing a delimiter-prefixed non-name into a name field.
- **56 duplicate-name author groups** — filed in #2416 (merged), split into three
  kinds. 🚨 The fragment carries an explicit warning: do NOT write one op that
  treats all three the same. Type 3 (real duplicates) wants a merge; types 1 and
  2 (book titles and `CD 13`-style disc labels stored as authors) want the author
  link removed. Merging everything would consolidate junk and make it look
  intentional.

---

## 7. Test-suite hazard — unresolved

`go test ./internal/server` (the ROOT package, not the handler subpackages)
**hangs and dies at exactly the timeout**, in both forms:

    full:  FAIL github.com/falkcorp/audiobook-organizer/internal/server  600.939s
    short: FAIL github.com/falkcorp/audiobook-organizer/internal/server  300.790s

The goroutine dump shows Pebble `vfs.(*diskHealthCheckingFile).startTicker`
(`disk_health.go:249/254`). Failing at *exactly* the timeout is the known
intermittent-stall signature recorded for this repo (usually `internal/database`
is the victim; here it is `internal/server`). 14 other packages pass.

**I did NOT establish whether this is pre-existing.** I launched a control run on
unmodified main and the session was interrupted before it finished, so treat
this as an open question, not as "known environmental." My change is confined to
`internal/server/handlers/audiobooks`, and both that package and
`internal/audiobooks` pass — but that is an argument, not the control.

**Next step:** run `go test -short -timeout 300s ./internal/server` on clean
main. If it hangs there too, it is pre-existing and CI's `Minimal CI / Go Tests
(short, race)` is the gate to trust.

---

## 8. Worktree / housekeeping state

- `../audiobook-organizer-bareparam` [`fix/bare-filter-param-set-derived`] —
  **live, has uncommitted work**, PR #2417 open.
- `../audiobook-organizer-invariant` and `../audiobook-organizer-dupauth` —
  already removed after their PRs merged.
- Two orphaned worktree dirs not registered with git, safe to delete:
  `.claude/worktrees/agent-a65e96827e3098a00`,
  `.claude/worktrees/agent-aa7e5c1c9192b7beb`.
- Two peer sessions were live in this repo (`ao-fixes-2`, `ao-fixes-3`).
  `ao-fixes-2` merged my #2416. **Run `gh pr list` before starting work** — I
  duplicated #2407/#2408 against a peer's #2411 earlier by checking only
  `git worktree list`, which shows branch names but not open PRs.

---

## 9. Method notes that earned their place today

- **Verify the instrument before believing the result.** Three separate
  instruments were broken today (`version_group_id` ignored, `is_primary_version`
  path-divergent, author listing primary-only). Each looked like a fact about the
  data. A negative control — a deliberately bogus value that should return
  nothing — caught all three.
- **A green test proves nothing until you have watched it go red.** The #2417
  conformance test was mutation-checked by dropping one field.
- **Count the subtests.** A table that silently iterates nothing reports success.
- **`cmd | tail` reports `tail`'s status.** I recorded `BUILD_EXIT=0` from a
  pipeline once today; the build had not been checked at all.
- **Grep every accessor spelling, not one.** `ParseQueryString(c, "x")` does not
  match `ParseQueryString\("`. This bit both `ab04824e` and me.
- **A global collision count over-reports.** 5 collisions across the server tree,
  1 on the endpoint that matters — scope the question to the route.
