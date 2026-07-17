<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-28-doc-drift-cleanup.md -->
<!-- version: 1.0.0 -->
<!-- guid: 17de70d5-3c21-4056-8d1b-d0143ad494ac -->
<!-- last-edited: 2026-07-03 -->

# TASK-28 — Doc-drift cleanup (AI-REFERENCE, pebble-schema duplicate, mockery pins)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-28-doc-drift-cleanup" -b agent/cr-28-doc-drift-cleanup origin/main
cd "$REPO/.worktrees/cr-28-doc-drift-cleanup"
git rebase origin/main
```

## Goal

Fix four independently-verified doc-drift defects (consultancy findings
PROC-3, PROC-5, ARCH-4, SYS-6). This is a **pure docs task** — no Go code
changes, no behavior changes. Every claim below was verified against the
current codebase on 2026-07-03; **re-verify with the grep commands given**
before editing, because line numbers drift.

1. `docs/AI-REFERENCE.md` header route count is stated as 189 but the real
   count (grep of gin route registrations) is ~395, and the file never
   mentions the Ollama/bge-m3 local-embedding cutover — it still presents
   OpenAI as the AI/embedding backend in three places.
2. `docs/AI-REFERENCE.md` contradicts itself: line 53 says the embedding/dedup
   store lives in "a separate PebbleDB"; line 357 correctly says it lives
   "within the main audiobooks.pebble DB". Code confirms the keyspace is
   **shared**, not separate.
3. `docs/database-pebble-schema.md` physically contains the entire document
   concatenated twice (a second `# PebbleDB Keyspace Schema and Data Model`
   header appears partway through, HTML-entity-corrupted in spots), and its
   "Conventions" section claims all entity IDs are ULID strings — production
   code actually uses integer IDs (`author:%d`, `book:%d`) for those entities.
   The **one part of the duplicated block that IS accurate** is the "Dedup /
   Embedding store" subsection (id16hex-keyed `dedup:`/`emb:` candidates) —
   that subsection only exists in the second (duplicate) copy and must be
   preserved, not deleted with the rest.
4. Two docs under `docs/agent-tasks/ci-flaky-fixes/` still teach agents that
   CI pins mockery at `v2.53.6` (`go install .../mockery/v2@v2.53.6`). CI,
   the Makefile, and `scripts/setup-mockery.sh` have pinned `v3.7.1` since PR
   #1718 (2026-07-01, "Mock Freshness" — already ✅ FIXED per TODO.md). An
   agent following either doc verbatim would reintroduce the exact
   repo-wide-mock-churn footgun the docs warn about.

## Background (verify before editing)

**Re-verify every claim below before touching a file — do not trust the line
numbers here, they are a snapshot from 2026-07-03.**

### 1 & 2 — `docs/AI-REFERENCE.md`

```bash
grep -n "Total API routes\|Last updated" docs/AI-REFERENCE.md
grep -c '\.GET(\|\.POST(\|\.PUT(\|\.DELETE(\|\.PATCH(' internal/server/*.go
grep -rn '\.GET(\|\.POST(\|\.PUT(\|\.DELETE(\|\.PATCH(' --include='*.go' internal/server | grep -v _test.go | wc -l
grep -n "OpenAI API\|OpenAIParser\|OpenAI batch API" docs/AI-REFERENCE.md
grep -n "separate PebbleDB for embeddings\|within the main audiobooks.pebble DB" docs/AI-REFERENCE.md
grep -n "func NewEmbeddingStore\|func (p \*PebbleStore) DB(" internal/database/embedding_store.go internal/database/pebble_store.go
grep -n "NewEmbeddingStore(ps.DB())" internal/server/registry_wire.go
```

As of this writing:
- `docs/AI-REFERENCE.md:6` says `**Total API routes**: 189`. The real count
  (non-test `.GET/.POST/.PUT/.DELETE/.PATCH` registrations under
  `internal/server`) is **395**. Do not hardcode a new magic number that will
  drift again — replace it with a pointer to how to compute it.
- `docs/AI-REFERENCE.md:31, 85, 451` mention OpenAI as the AI/embedding
  backend (`→ AI parser → OpenAI API`, `OpenAIParser` method list, "AUTHOR
  dedup via OpenAI batch API"). None of these are wrong on their own (the
  `OpenAIParser` type still exists and is still used for some flows) — but the
  doc has **zero** mention of Ollama or bge-m3, even though
  `internal/ai/embedding_client.go`, `internal/database/hnsw_embedding_store.go`,
  `internal/dedup/engine.go`, and `internal/server/registry_wire.go` all
  reference `ollama`/`bge-m3` for the current embedding backend (per the local
  embeddings cutover). The doc is silently incomplete, not necessarily
  factually wrong about what remains of OpenAI usage — do not delete the
  OpenAI mentions, add the missing Ollama/bge-m3 context alongside them.
- `docs/AI-REFERENCE.md:53` — `EmbeddingStore` "wrapping **a separate
  PebbleDB**". `docs/AI-REFERENCE.md:357` — the `emb:`/`dedup:` keyspace is
  "**within the main audiobooks.pebble DB**". These two lines contradict each
  other in the same file. Verified in code: `registry_wire.go:69` constructs
  it as `database.NewEmbeddingStore(ps.DB())`, and `PebbleStore.DB()`
  (`internal/database/pebble_store.go`) returns the store's own live
  `*pebble.DB` handle — i.e. the SAME database file as everything else, not a
  second one. Line 357 is correct; line 53 is the drift. Fix line 53 only —
  do not touch line 357's wording (it's already right), just make sure the two
  don't disagree after your edit.

### 3 — `docs/database-pebble-schema.md`

```bash
grep -n "^# PebbleDB Keyspace Schema and Data Model\|^## Dedup / Embedding store\|^## Conventions" docs/database-pebble-schema.md
wc -l docs/database-pebble-schema.md
grep -n "ULID strings" docs/database-pebble-schema.md
grep -n "id16hex" docs/database-pebble-schema.md
```

As of this writing, the file is 645 lines and the top-level header
`# PebbleDB Keyspace Schema and Data Model` appears **twice**: once at line 6
(the file's real start) and again at line 317, i.e. the whole document is
concatenated with itself starting around line 311/312. The second copy has
HTML-entity corruption in places (literal `&lt;`/`&gt;` instead of `<`/`>`,
and its own embedded stray file-header comment block around line 311-314).

**Do not blindly delete lines 312-645.** The second copy is not a pure
redundant repeat — it contains one section the first copy is missing
entirely: `## Dedup / Embedding store (within the main audiobooks.pebble DB)`
(around line 561-599 as of this writing), covering the `emb:`/`dedup:`
keyspace with `id16hex` (`fmt.Sprintf("%016x", uint64(candidateID))`)
zero-padded hex keys. This subsection is the **one part of the whole
document verified accurate against code** (`internal/database/embedding_store.go`,
`internal/dedup/engine.go`). Everything else in the file (both copies) — the
`## Conventions` section's "IDs: ULID strings (26-char Crockford base32)" and
the ULID-keyed `a:`/`b:`/`u:`/`s:`/`w:` prefixes in `## Key prefixes` — was
**never implemented**; production code uses integer IDs (verify below).

```bash
grep -n 'LowerBound: \[\]byte("author:0")\|LowerBound: \[\]byte("book:0")' internal/database/pebble_store.go
grep -n 'fmt.Sprintf("author:%d"\|fmt.Sprintf("book:%d"' internal/database/pebble_store.go
```

Confirms `internal/database/pebble_store.go` uses `author:%d` / `book:%d`
integer-keyed prefixes with ASCII-range iteration bounds (`"author:0"` ..
`"author:;"`), not ULID strings.

`docs/database-architecture.md` (around lines 17-27) calls
`database-pebble-schema.md` "canonical for any new feature touching
persistence" — re-verify with:
```bash
grep -n "canonical for any new feature" docs/database-architecture.md
```

### 4 — mockery version docs

```bash
grep -rn "2.53.6" docs/agent-tasks/ci-flaky-fixes/
grep -n "Install mockery\|mockery/v3@v3.7.1" .github/workflows/ci.yml
grep -n "MOCKERY_VERSION" scripts/setup-mockery.sh
grep -n "mock-freshness\|Mock Freshness" TODO.md
```

As of this writing, CI (`.github/workflows/ci.yml`, "Install mockery" step)
and `scripts/setup-mockery.sh` both pin `v3.7.1` (module
`github.com/vektra/mockery/v3`), and TODO.md's "Mock Freshness" entry is
marked `✅ FIXED (#1718, 2026-07-01)`. But
`docs/agent-tasks/ci-flaky-fixes/README.md` and
`docs/agent-tasks/ci-flaky-fixes/TASK-01-mockery-pin.md` both still instruct
`go install github.com/vektra/mockery/v2@v2.53.6` and describe the v2/v3 pin
drift as an open problem to fix. `TASK-01` in that workstream is the exact
"mock-freshness" task TODO.md says is already done.

Note: `docs/agent-tasks/ci-flaky-fixes/README.md`'s existing header comment is
itself HTML-entity-corrupted (`&lt;!-- file: ... --&gt;` instead of
`<!-- file: ... -->`) — when you bump its header per file-headers.md, write it
with real `<!--`/`-->` delimiters, which fixes this incidentally.

## Step-by-step

1. **`docs/AI-REFERENCE.md`**:
   - Re-run the route-count grep from Background. Replace the hardcoded route
     count in the header line with either the freshly-verified number or (
     preferred, so it can't drift again) a phrase like "**Total API routes**:
     see `grep -rn '\.GET(\|\.POST(\|\.PUT(\|\.DELETE(\|\.PATCH(' --include='*.go' internal/server | grep -v _test.go | wc -l`
     for current count (~395 as of 2026-07-03)".
   - Fix line 53 (`EmbeddingStore` description) to say the store is backed by
     "the main audiobooks.pebble DB" (or equivalent wording), matching line
     357. Do not edit line 357.
   - Add a short new subsection (near the existing AI-integration section
     around line 85, or immediately after it) titled something like
     "Embedding/LLM backends" documenting: Ollama is the primary local
     embedding + LLM backend (bge-m3, 1024-dim vectors; see
     `internal/ai/embedding_client.go`, `internal/dedup/engine.go`,
     `internal/database/hnsw_embedding_store.go`); OpenAI (`OpenAIParser`,
     `internal/ai/openai_parser.go`) remains in use for the flows already
     documented nearby — do not claim OpenAI was fully removed, only that
     Ollama is the current embedding/LLM path added on top of it. Keep it to a
     few lines; this is a pointer, not a full rewrite.
   - Bump the file header (version + `last-edited`).

2. **`docs/database-pebble-schema.md`**:
   - Re-run the header-count and Dedup-section greps from Background to
     locate the current duplicate boundary and the `## Dedup / Embedding
     store` subsection's exact current line range — the ranges here (312-645,
     561-599) are 2026-07-03 snapshots and may have shifted if anyone touched
     the file since.
   - Copy the entire `## Dedup / Embedding store (within the main
     audiobooks.pebble DB)` subsection (including its `### Embedding
     keyspace`, `### Dedup candidate keyspace`, `### Labeled dataset
     keyspace` children, and the `id16hex` explanation paragraph) out of the
     duplicate block.
   - Delete the whole duplicate block (the second copy of the document,
     starting at its repeated `# PebbleDB Keyspace Schema and Data Model`
     header through end-of-file), **except** for the section you just copied
     out.
   - Insert the copied `## Dedup / Embedding store` section into the
     first (retained) copy, placed after its `## Query patterns` section and
     before `## Write patterns & atomicity` (matching where it sits in the
     original second copy, relative to the sections that already exist once
     in the first copy).
   - In the `## Conventions` section (first copy, "IDs: ULID strings..."
     bullet), add a clarifying note that this ULID design was never
     implemented for the core entities — production code uses integer IDs
     (`author:%d`, `book:%d`, etc., see `internal/database/pebble_store.go`)
     — and that the `## Dedup / Embedding store` section's `id16hex` keys are
     the one part of this document that matches the live schema. Do not
     attempt to rewrite the rest of the document's ULID-based entity sections
     to match reality in this task — that is a much larger regeneration
     effort (tracked separately per the consultancy recommendation); this task
     only removes the corrupted duplicate and adds an accurate caveat.
   - Confirm the file no longer contains a second top-level `#
     PebbleDB Keyspace Schema and Data Model` header and no `&lt;`/`&gt;`
     entity artifacts remain (`grep -c '&lt;\|&gt;' docs/database-pebble-schema.md`
     should be `0`).
   - Bump the file header (version + `last-edited`).

3. **`docs/database-architecture.md`**:
   - Re-verify with `grep -n "canonical for any new feature"
     docs/database-architecture.md`.
   - Add one short caveat sentence next to the existing "canonical for any
     new feature touching persistence" claim, noting that the linked doc's
     entity-ID convention (ULID) does not match the current integer-ID
     implementation, and pointing at the `## Dedup / Embedding store` section
     of that doc as the part that IS verified accurate. Keep this to 1-2
     sentences — do not restructure the file.
   - Bump the file header (version + `last-edited`).

4. **`docs/agent-tasks/ci-flaky-fixes/README.md`** and
   **`docs/agent-tasks/ci-flaky-fixes/TASK-01-mockery-pin.md`**:
   - Re-run `grep -rn "2.53.6" docs/agent-tasks/ci-flaky-fixes/` to confirm
     current occurrences.
   - In both files, replace every `mockery/v2@v2.53.6` reference (install
     commands, prose, commit-message text, idempotency-check text) with the
     current pin: `go install github.com/vektra/mockery/v3@v3.7.1` /
     `v3.7.1` (module `github.com/vektra/mockery/v3`), matching
     `.github/workflows/ci.yml`'s "Install mockery" step and
     `scripts/setup-mockery.sh`.
   - Since TODO.md already marks "Mock Freshness" ✅ FIXED (#1718), add a note
     at the top of `README.md`'s TASK-01 table row (or directly above it)
     stating TASK-01 is already done/superseded by #1718 and should be
     treated as archived — do not remove the row (keep it as workstream
     history) but make it clearly not-actionable for a new agent. Do the same
     at the top of `TASK-01-mockery-pin.md` (a one-line "DONE — see #1718"
     banner near the title is sufficient; do not delete the rest of the file's
     content, it remains useful history).
   - When bumping `README.md`'s file header, write plain `<!-- ... -->`
     comment delimiters (fixing the pre-existing `&lt;!--`/`--&gt;`
     entity-corruption noted in Background as a side effect).
   - Bump the file header (version + `last-edited`) on both files.

## How to test

This is a docs-only change — no Go build/test coverage applies to prose, but
confirm nothing else broke:

```bash
go build ./...
go vet ./...
grep -c "PebbleDB Keyspace Schema and Data Model" docs/database-pebble-schema.md   # expect: 1
grep -c '&lt;\|&gt;' docs/database-pebble-schema.md                                # expect: 0
grep -c '&lt;\|&gt;' docs/agent-tasks/ci-flaky-fixes/README.md                     # expect: 0
grep -n "v2.53.6" docs/agent-tasks/ci-flaky-fixes/README.md docs/agent-tasks/ci-flaky-fixes/TASK-01-mockery-pin.md   # expect: no matches
grep -n "id16hex" docs/database-pebble-schema.md                                   # expect: still present
grep -n "separate PebbleDB" docs/AI-REFERENCE.md                                   # expect: no matches
```

## Acceptance criteria

- [ ] `docs/AI-REFERENCE.md` header route count is no longer the stale `189`
      (either a corrected number or a self-updating pointer/grep command).
- [ ] `docs/AI-REFERENCE.md` no longer says the embedding store lives in "a
      separate PebbleDB"; it matches the (unedited) line 357 claim that it's
      in the main audiobooks.pebble DB.
- [ ] `docs/AI-REFERENCE.md` has a new short section mentioning
      Ollama/bge-m3 as the current embedding/LLM backend, without deleting
      the existing (still-valid) OpenAI mentions.
- [ ] `docs/database-pebble-schema.md` contains exactly one copy of the
      document (one `# PebbleDB Keyspace Schema and Data Model` header) and
      zero `&lt;`/`&gt;` entity-corruption artifacts.
- [ ] `docs/database-pebble-schema.md` still contains the `## Dedup /
      Embedding store` section with its `id16hex` explanation — it was
      preserved, not deleted along with the rest of the duplicate block.
- [ ] `docs/database-pebble-schema.md`'s `## Conventions` section now notes
      the ULID entity-ID design was never implemented (production uses
      integer IDs) and that the Dedup/Embedding section is the verified-
      accurate part.
- [ ] `docs/database-architecture.md` has a short caveat next to its
      "canonical" claim pointing out the ULID/integer-ID mismatch.
- [ ] Neither `docs/agent-tasks/ci-flaky-fixes/README.md` nor
      `docs/agent-tasks/ci-flaky-fixes/TASK-01-mockery-pin.md` mentions
      `v2.53.6`/`mockery/v2` as the pin anymore; both reference `v3.7.1`
      (`mockery/v3`), matching CI/Makefile/setup script.
- [ ] Both mockery docs clearly flag TASK-01/mock-freshness as already done
      (#1718) rather than presenting it as open work.
- [ ] `go build ./...` and `go vet ./...` still pass (should be unaffected —
      docs-only change; this just confirms nothing else in the worktree is
      broken).
- [ ] File headers bumped on every changed file: `docs/AI-REFERENCE.md`,
      `docs/database-pebble-schema.md`, `docs/database-architecture.md`,
      `docs/agent-tasks/ci-flaky-fixes/README.md`,
      `docs/agent-tasks/ci-flaky-fixes/TASK-01-mockery-pin.md`.

## Commit message

```
docs: fix AI-REFERENCE drift, dedupe pebble-schema doc, update mockery pins (consultancy-roadmap)

AI-REFERENCE.md had a stale ~2x-off route count, no mention of the Ollama/
bge-m3 local-embedding cutover, and a self-contradiction on where the dedup/
embedding keyspace lives. database-pebble-schema.md contained a corrupted
duplicate of itself with a never-implemented ULID key convention; the
duplicate is removed while preserving its one verified-accurate section
(id16hex-keyed dedup/embedding candidates). Two ci-flaky-fixes docs still
taught the old mockery v2.53.6 pin after CI/Makefile moved to v3.7.1 (#1718),
risking a repeat of the exact mock-churn footgun they warn about.

Co-Authored-By: Claude Haiku <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-28-doc-drift-cleanup
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Before starting, re-run the Background greps. If
`docs/database-pebble-schema.md` already has only one top-level header and no
`&lt;`/`&gt;` artifacts, if `docs/AI-REFERENCE.md`'s route count and
embedding-store description already match code, and if neither mockery doc
mentions `v2.53.6`, this task is already done — do not re-edit, just confirm
and stop. If any single sub-item (e.g. only the mockery docs) is already
fixed but others aren't, do only the remaining sub-items — each of the four
fixes is independent and can be committed/skipped separately without
affecting the others. Rollback = revert the commit; this is a pure docs
change with no runtime or schema effect, so revert is always safe.
