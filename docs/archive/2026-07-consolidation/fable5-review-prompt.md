# Fable 5 Full Review + Refactor Spec Prompt

## Your Role

You are the **lead architect and reviewer** for `audiobook-organizer`, a full-stack audiobook management system (Go 1.24 backend + React/TypeScript frontend). Your job is to produce a **complete, spec-quality written deliverable**. The deliverable will be used by Sonnet subagents to do the actual work, so every task must be self-contained, idempotent, and explained at the level where "no one in their right mind could fuck it up."

### What you may and may not do

- **DO NOT implement features.** Do not write or modify `.go`, `.ts`, `.tsx`, `.js`, `.sql`, `.yaml`, `.json`, or any other source/config files. Implementing the specs is Sonnet's job, not yours. Your *code* output is zero.
- **DO run read-only analysis to ground every claim in evidence.** Build and run the existing `cmd/itl-*` diagnostic tools, run `grep`/`go build`/`go vet`, read source, and binary-diff the iTunes libraries. Investigation is not implementation — you are *encouraged* to run tools, you are *forbidden* to ship code. Empirical evidence beats theorizing every time; a claim you verified by running a tool is worth ten you reasoned about.
- **The only files you create are documentation** (specs, plan, TODO/CHANGELOG updates).

### Where your output goes

**Do NOT dump the full specs into chat.** Write each deliverable to its own file *the moment it is complete* (see "write-as-you-go" below), then reply with a short summary + the list of file paths. Long inline content is explicitly against this repo's conventions.

- Specs → `docs/specs/` (one file per spec)
- Implementation plan → `docs/plans/`
- `TODO.md` — add a new section for each spec with task stubs
- `CHANGELOG.md` — **prepend** an entry (never replace the body) describing the review and planned work

### Git workflow (MANDATORY — do not commit to main)

This repo forbids committing directly to `main`. Before writing any file:
1. Create a worktree + branch: `git worktree add ../audiobook-organizer-fable5-review -b docs/fable5-review` and `cd` into it. Confirm with `git worktree list`.
2. Do all writing inside that worktree.
3. Commit there with a conventional commit: `docs(review): fable5 full-system review, specs, and implementation plan`.
4. Push the branch and open a PR for review (`gh pr create`). Do **not** merge it — the owner reviews and ships. Report the PR URL, branch name, and worktree path in your summary.

### Write-as-you-go (protects against context exhaustion)

This is a large task. Write each spec to its file **as soon as that spec is complete**, before starting the next one — do not hold all output in memory to write at the end. If you run low on context, completed specs are already on disk. Commit incrementally if helpful.

---

## Codebase Cheat Sheet

> **This cheat sheet was compiled from project memory and may be stale.** Treat it as a fast-start map, not ground truth. Before you spec against any specific detail — line counts, constant values (e.g. `LSHBandCount`), PR numbers, function names, key prefixes — verify it against the actual code with a quick `grep`/`Read`. If a fact here contradicts the code, the code wins; note the discrepancy in your findings.

### Stack

- **Backend:** Go 1.24 (will upgrade to 1.25), REST + SSE, embedded React frontend via `//go:embed web/dist`
- **Frontend:** React + TypeScript + Material-UI, Vite build, Vitest tests, Playwright E2E
- **Primary DB:** PebbleDB (key-value, at `/var/lib/audiobook-organizer/audiobooks.pebble`) — the only production DB we fully trust
- **Secondary DBs (want to reduce):** SQLite (`embeddings.db`, `ai_scans.db`), NutsDB (`activity.nutsdb`, `metrics.nutsdb`), Bleve (full-text search at `library.bleve`)
- **Production:** Linux at `172.16.2.30`, ~50K books (10,891 organized + ~39K iTunes-imported), systemd service
- **Build:** `make build` (frontend first, then `go build -tags embed_frontend`), `make test`, `make ci` (80% coverage gate)

### Key Package Map

```
internal/
  dedup/           — 3-layer dedup engine (hash → embedding → LLM), book/author/series dedup
  fingerprint/     — AcoustID fingerprint extraction (fpcalc), LSH bucketing, whole-file hash
  acoustid/        — AcoustID API client (chromaprint lookup against MusicBrainz)
  itunes/          — .itl binary parser/writer (LE + BE formats), writeback, path repair
  itunes/service/  — iTunes sync service: importer, path reconcile, playlist sync, validate
  plugins/dedup/   — UOS plugin wrapper: embed-scan, full-scan, llm-review, book-sig-scan, split-book-scan
  plugins/itunes/  — UOS plugin wrapper: iTunes sync operations
  plugins/acoustid/— UOS plugin wrapper: acoustid backfill
  scanner/         — File scanner, dedup.go integration
  metadata/        — Tag read/write (taglib), Audible enrichment, custom AUDIOBOOK_ORGANIZER_* tags
  merge/           — Book merge, collision detection
  database/        — Store interface + PebbleDB implementation + EmbeddingStore (SQLite)
  server/handlers/ — HTTP handlers (extracted from *Server in PR #1232+)
    dedup/         — Dedup HTTP handlers
    duplicates/    — Duplicates HTTP handlers

web/src/
  pages/BookDedup.tsx        — Main dedup page (multi-tab: Books, Authors, Series, Reconcile, Split)
  components/dedup/          — DedupBookTab, DedupAdvancedScanTab, DedupAuthorTab, DedupSeriesTab, DedupReconcileTab, DedupSplitBookTab
  components/FingerprintVisualsColumn.tsx — Fingerprint display in book detail
  components/settings/ITunesImport.tsx   — iTunes settings panel
```

### Current Dedup Architecture (The Silo Problem)

The dedup system currently has **5 separate, disconnected signal sources** that are exposed to the user as separate UX surfaces:

| Signal Source | Where computed | User sees it |
|---|---|---|
| File hash (SHA-256 or similar) | `internal/database`, `internal/dedup/book_dedup.go` → `store.GetDuplicateBooks()` | BookDedup "Books" tab, confidence="high" |
| Folder/path duplicates | `store.GetFolderDuplicates()` | BookDedup "Books" tab, confidence="medium" |
| Metadata fuzzy (title 0.85 threshold) | `store.GetDuplicateBooksByMetadata(0.85)` | BookDedup "Books" tab, confidence="low" |
| Embedding cosine similarity | `internal/dedup/engine.go` Layer 2, `database.EmbeddingStore` (SQLite) | BookDedup "Advanced Scan" tab (separate) |
| AcoustID whole-file chromaprint | `internal/fingerprint/` + `internal/acoustid/`, LSH in `fingerprint/lsh.go` | BookDetail fingerprint column, NOT exposed in dedup UI |

**The Engine's 3-layer model:**
```
Layer 1: Exact matching — file hash, ISBN/ASIN exact match, near-identical titles
Layer 2: Embedding similarity — cosine similarity via OpenAI embeddings + chromem (in-RAM)
Layer 3: LLM review — batch OpenAI calls for ambiguous candidates (0.85-0.95 range)
```

**AcoustID status:**
- Tier-1 exact: O(1) via `book_file_acoustid:` PebbleDB index (works, fast)
- Tier-2 fuzzy: O(N) scan disabled by default (`ACOUSTID_FUZZY_ENABLED=1` env var); LSH bucketing (Step 3, `fpidx:<subfp>:<bookfile_id>` PebbleDB secondary index) is designed but NOT YET IMPLEMENTED
- LSH constants defined in `fingerprint/lsh.go`: `LSHBandCount=64`, `LSHSubprintBytes=8`, `LSHMinBandHits=2`

**Scoring design note (intentional, NOT a bug):**
Similarity scores are allowed to exceed 1.0 (100%). The design intention is bonus scoring: e.g., matching series number = +bonus%, metadata from Audible = +bonus%. This is stored as `float64` in `DedupCandidate.Similarity`. The reviewer should evaluate whether to keep >100% or normalize to 100% max with percentile bonuses inside — whichever is smarter for producing a near-98% correct identification rate.

### iTunes Integration Architecture

**Format:** Apple's proprietary `.itl` binary format — NOT XML. Two variants:
- **BE (Big-Endian):** older iTunes, PowerPC-era, tag `hdfm` prefix
- **LE (Little-Endian):** modern iTunes (v10+), tag `msdh` containers, `mith` track blocks, `miph`/`mtph` playlist items

**Key files:**
- `internal/itunes/itl.go` — top-level parser, format auto-detect
- `internal/itunes/itl_le.go` (688 lines) — LE chunk walker
- `internal/itunes/itl_be.go` (556 lines) — BE parser
- `internal/itunes/itl_le_mutate.go` — mutation (track updates, removals)
- `internal/itunes/itl_le_verify.go` — consistency check (dangling playlist refs)
- `internal/itunes/itl_le_repair.go` — repair dangling refs
- `internal/itunes/rebuild.go` — library rebuild from XML export
- `internal/itunes/service/` — sync service, path repair, playlist sync, validate

**Known corruption class (May 2026):**
`RemoveTracksByPIDLE` previously excised `mith` blocks but left orphaned `mtph` items in playlists. `VerifyITLNoNewDanglingRefsLE` now guards against this, but only checks for NEW dangling refs introduced by a write (not pre-existing ones). `VerifyITLNoDanglingRefsLE` checks all.

**Critical discovery:** iTunes will open a semi-broken library, but "Apple Devices" (the Windows sync app for iPhone) CRASHES if there are corrupted tracks. This is the production blocker. Need rock-solid writeback with comprehensive safeguards.

**iTunes Library Files (pre-pulled to `/tmp/itunes-libraries/` for your analysis):**
```
/tmp/itunes-libraries/iTunes Library.itl          — 30.8MB, GOLDEN COPY (fully managed by iTunes, works perfectly)
/tmp/itunes-libraries/writeback-iTunes Library.itl — 1.0MB, our writeback test library (small)  
/tmp/itunes-libraries/damaged-1.itl               — 29.0MB, iTunes Library (Damaged).itl
/tmp/itunes-libraries/damaged-2.itl               — 29.0MB, iTunes Library (Damaged) 1.itl
/tmp/itunes-libraries/damaged-3.itl               — 29.3MB, iTunes Library (Damaged) 2.itl
/tmp/itunes-libraries/damaged-4.itl               — 29.7MB, iTunes Library (Damaged) 3.itl
```

The golden copy (`iTunes Library.itl`) works perfectly as it has been managed entirely by iTunes. Compare its structure against the damaged ones to understand what makes an iTunes library valid. The damaged files are the result of our writeback operations going wrong — iTunes renamed them with "(Damaged)" when it detected corruption on open.

**Also relevant:** `cmd/itl-check/`, `cmd/itl-diff/`, `cmd/itl-repair/`, `cmd/itl-roundtrip/`, `cmd/itl-write-test/`, `cmd/itunes-sync-tests/` — diagnostic CLI tools already exist for inspecting .itl files.

### Memory / Database Pain Points

Known issues (from project memory + code comments):
1. **Cache warm-up memory bloat:** Disabled after 69GB peak RAM usage. Root cause: storing full API response objects in cache. Proper fix pending: refactor to store minimal data.
2. **EmbeddingStore (SQLite):** Stores OpenAI embeddings for ~50K books. SQLite single-writer causes lock contention. Schema: `embeddings(book_id, embedding_blob, updated_at)`.
3. **Bleve index (`library.bleve`):** Full-text search. Not heavily optimized.
4. **Library counts cache:** Single k:v at `stats:library` (PebbleDB) with dirty flag + 10-min min-recompute interval. Works but was causing thrash before fix in PR #1072.
5. **Memdb strip:** `BookFile.AcoustIDFingerprint []byte` is stripped before storing in memdb to protect RSS (documented in PR fingerprint-wholefile).
6. **NutsDB:** Used for activity log and metrics. Compaction via `RecompactDigests`. Generally working but adds complexity.

### Scoring & Matching Domain Rules

- `tag_priority`: album_artist > artist > composer (composer = narrator in audiobooks)
- Audible metadata = highest quality source, gives bonus to any match
- ISBN/ASIN exact match = very high confidence
- Series number match = bonus points
- Duration match within ±2% = strong signal for same-content detection
- Scores intentionally allowed >100% for bonus stacking (by design — owner is OK changing this)
- `UpdateBook` does FULL column replacement — be careful with partial updates

### Git / Build Discipline

- All changes go through PRs; never commit to main directly
- Rebase/FF only (no squash merges)
- Version headers on every file: `// file: path`, `// version: x.y.z`, `// guid: uuid`
- `make ci` is the gate (all tests + 80% coverage)
- Conventional commits mandatory

---

## Your Mission

Produce a **complete, written spec and implementation plan** covering the 4 priorities below. Output format:

1. **CODEBASE REVIEW FINDINGS** — security issues, bugs, anti-patterns (organized by severity: CRITICAL / HIGH / MEDIUM / LOW)
2. **SPEC: Unified Dedup Pipeline** — full architecture spec for the combined identification + dedup system
3. **SPEC: iTunes Writeback Hardening** — full architecture spec for rock-solid iTunes library safety
4. **SPEC: Memory + DB Optimization** — concrete schema and architecture improvements
5. **IMPLEMENTATION PLAN** — broken into discrete tasks, each suitable for a single Sonnet subagent

---

## Priority 1: Unified Identification & Deduplication Pipeline

### Problem

Users currently must visit multiple separate surfaces to see all dedup signals for a book:
- "Dedup" → "Books" tab: sees hash/folder/metadata results
- "Dedup" → "Advanced Scan" tab: sees embedding similarity results
- BookDetail page: sees fingerprint data but it's NOT connected to dedup decisions
- AcoustID results exist in PebbleDB but are NOT shown in the dedup UI at all
- LSH bucketing (the planned Step 3) is designed but not implemented

Users must manually correlate across these. There is no unified "is this a duplicate?" answer.

### Goal

Design a **Unified Dedup Tab** that combines all signal sources into a single composite score per candidate pair. The unified score should:

1. **Hierarchy of evidence** (highest to lowest confidence):
   - `EXACT_FILE`: identical whole-file hash → certainty ~100%
   - `EXACT_ACOUSTID`: identical AcoustID chromaprint (tier-1 exact PebbleDB lookup) → certainty ~99%
   - `LSH_ACOUSTID`: LSH bucket collision (≥2 band hits) + Hamming similarity → certainty ~90-97%
   - `ISBN_ASIN_MATCH`: exact ISBN-10/13 or ASIN match → certainty ~98%
   - `EMBEDDING_HIGH`: cosine similarity ≥ 0.95 → certainty ~88-95%
   - `METADATA_FUZZY`: title/author normalized fuzzy match (Levenshtein or similar) → certainty ~70-85%
   - `DURATION_MATCH`: duration within ±2% → supporting signal only, not standalone
   - `EMBEDDING_MEDIUM`: 0.85 ≤ cosine < 0.95 → certainty ~65-80%
   - `FOLDER_PATH`: same directory → low confidence alone, high in combination

2. **Scoring formula design:** Propose a specific weighted scoring system. Owner is OK with scores staying >100% (bonus stacking) OR normalizing to 100% max — choose whichever produces better practical results at ~98% correct identification. The formula must be:
   - Explainable (user can see which signals contributed and how much)
   - Configurable (thresholds in config.yaml, not hardcoded)
   - Auditable (score breakdown stored per candidate)

3. **LSH implementation:** Step 3 (`fpidx:<subfp>:<bookfile_id>` PebbleDB secondary index) must be fully designed and specced. The LSH infrastructure already exists in `fingerprint/lsh.go` — it just needs the PebbleDB index built and the dedup engine wired to use it instead of the O(N) fuzzy scan.

4. **UI:** Design a single unified dedup view that shows:
   - Composite score with breakdown (which signals fired, each signal's contribution)
   - Confidence level: CERTAIN / HIGH / MEDIUM / REVIEW (maps to action buttons: auto-merge / suggest-merge / review-needed / skip)
   - Side-by-side comparison: cover art, metadata, file info, audio sample
   - Score badges (e.g. "AcoustID ✓", "Hash ✓", "ISBN ✓", "Embedding 94%")

5. **Dedup scan operations:** Rationalize the current set of dedup plugin operations (`embed-scan`, `embed-async`, `full-scan`, `llm-review`, `book-signature-scan`, `split-book-scan`, `purge-stale`) into a coherent workflow. Which should run automatically? Which on-demand? What's the recommended scan order?

### Spec Requirements

- Full interface definition for `UnifiedDedupScore` struct (Go)
- Algorithm pseudocode for composite score calculation
- LSH index schema in PebbleDB (key format, value format, build/update/delete lifecycle)
- API endpoint changes (new endpoint? extend existing `/api/v1/dedup/candidates`?)
- React component tree for the new unified tab
- Data migration plan (existing DedupCandidate rows need score backfill)

---

## Priority 2: iTunes Library Writeback Hardening

### Problem

iTunes will open a semi-broken library but Apple Devices (iPhone sync app) crashes on corrupt tracks. We have had multiple instances of our writeback producing "Damaged" libraries. The root cause was `RemoveTracksByPIDLE` excising `mith` blocks but leaving orphaned `mtph` items in playlists.

We have `VerifyITLNoNewDanglingRefsLE` and `VerifyITLNoDanglingRefsLE` which check for this specific case, but we need a comprehensive audit of EVERYTHING that can corrupt a library.

### PRECONDITION GATE (do this first)

Before any iTunes analysis, verify your evidence exists:
```bash
ls -la /tmp/itunes-libraries/        # expect golden + writeback + damaged-1..4
ls cmd/itl-diff cmd/itl-check         # diagnostic tools must be present
```
The empirical analysis below depends entirely on the damaged libraries being present on this machine (they are ephemeral, in `/tmp`). **If the damaged files or the `cmd/itl-*` tools are missing, STOP and report it — do not theorize iTunes corruption without the evidence.** A speculative corruption checklist is the explicit anti-goal of this section.

### Investigation Required (EMPIRICAL FIRST — evidence before theory)

1. **Diff the golden library against each damaged one. This is mandatory and comes first.** The files are at `/tmp/itunes-libraries/`. Build and run `cmd/itl-diff` (`go run ./cmd/itl-diff -v <golden> <damaged>`) for the golden vs. each of `damaged-1..4`, plus the writeback test library. Also run `cmd/itl-check` on each. Determine:
   - What structural differences exist between the golden `iTunes Library.itl` and each damaged file?
   - Are there field values, block types, or sequences in the golden copy that are absent or wrong in the damaged copies?
   - Is there a pattern? (Same block type corrupted across all 4 damaged files? Or different damage in each?)

   > **Tool blind spot — read this.** `cmd/itl-diff` compares the `hdfm` header, track/playlist *counts*, and per-track metadata **by Persistent ID**. It does **NOT** diff playlist *membership* or `mtph`/`miph` playlist-item refs — which is exactly the known corruption class (orphaned `mtph` items). So `itl-diff` reporting "0 tracks changed" does **NOT** mean a library is clean. For playlist-ref / dangling-ref corruption, use `cmd/itl-check` and the `VerifyITLNoDanglingRefsLE` / `VerifyITLNoNewDanglingRefsLE` paths, and fall back to direct binary/hex inspection of the `msdh` playlist containers where the tools don't reach. State which corruption types each tool can and cannot detect.

2. **Identify ALL ways a writeback can corrupt an iTunes library — then tie each one back to evidence.** Known one: dangling mtph refs. Build the candidate list below, but for **every** vector you list, mark it either `OBSERVED` (you saw it in a damaged-file diff — cite which file and the specific bytes/fields) or `SPECULATIVE` (plausible but not seen in these samples). Do not present a free-standing checklist of theoretical vectors; the OBSERVED set is what the safety contract must catch first. Consider:
   - Invalid TID sequences or gaps in track ID space
   - Malformed mhoh (metadata string) blocks (wrong length prefix, bad UTF-16 encoding)
   - Playlist sort order fields (`mnol`, `mpsl`) pointing at deleted tracks
   - Library header (`mshh`) checksum or track count fields that don't match reality
   - Persistent ID (`mhpid`) collisions or format violations
   - Smart playlist criteria (`smart_criteria`) with refs to non-existent fields
   - mith block ordering requirements (does iTunes require sorted by TID?)
   - BE-format libraries: does our BE writer have the same safeguards as the LE writer?

3. **"Apple Devices" crash specifics.** Based on the damaged library analysis, what is the specific field or structure that causes Apple Devices to crash (vs. iTunes just showing a warning)?

### Spec Requirements

1. **Write-guard contract:** Define a formal `ITLSafetyContract` — every writeback operation MUST pass all guards before bytes are committed to disk. Guards must be individually documented and testable.

2. **Atomic write protocol:** Design an atomic write-with-rollback protocol:
   - Write to `.itl.new` first
   - Run full safety contract validation on the new bytes
   - If validation passes: atomically rename `.itl.new` → `.itl` (after backing up `.itl` → `.itl.bak-<timestamp>`)
   - If validation fails: delete `.itl.new`, preserve original, return detailed error
   - Backup retention policy (how many baks? TTL?)

3. **Regression test suite design:** For each type of corruption found in the damaged libraries, there must be a corresponding regression test that:
   - Starts with a minimal valid .itl (generated by `generate_test_itls.go`)
   - Applies the problematic mutation
   - Asserts that the safety contract CATCHES it
   - Tests are idempotent and don't depend on real iTunes

4. **Safeguard gaps:** List every safeguard that currently DOES NOT exist but SHOULD. For each, spec the implementation.

5. **"Apple Devices" compatibility checklist:** A specific list of invariants that must hold in any valid .itl file that Apple Devices can sync from. This should be based on empirical analysis of the golden copy.

---

## Priority 3: Memory & Database Optimization

### Problem

- Peak RAM was 69GB (cache warm-up storing full API responses) — now disabled, but root cause not fixed
- Multiple databases add operational complexity: PebbleDB + SQLite (3 files) + NutsDB (2 files) + Bleve
- SQLite single-writer causes lock contention on high-throughput scans
- Embedding storage in SQLite has no compression; `embedding_blob` for 50K books at OpenAI's 1536-dim float32 = ~300MB raw
- Library counts cache approach works but is manually maintained

### Analysis Required

1. **PebbleDB schema audit.** Review the key namespace structure in `internal/database/pebble_store.go`. Identify:
   - Key prefixes that are never used or can be consolidated
   - Values that store full JSON structs when only a few fields are needed
   - Hot paths that read and deserialize full Book objects when only IDs/counts are needed
   - Estimate memory savings from each optimization

2. **Eliminate SQLite where possible.** `embeddings.db` specifically:
   - Can embeddings be stored in PebbleDB? Design the key schema (e.g., `embed:book:<id>` → compressed float32 vector)
   - What compression ratio can we expect? (Product quantization? Simple zlib?) Target: 10x reduction from ~300MB
   - Chromem (in-RAM embedding index) currently hydrated from SQLite on startup — how does this interact?

3. **NutsDB evaluation.** Can the activity log and metrics NutsDB stores be migrated to PebbleDB? What's the tradeoff?

4. **Bleve evaluation.** Full-text search on 50K books. Is Bleve the right tool? Could PebbleDB's prefix scans + SQLite FTS5 handle this better? What's the index size?

5. **In-memory object sizing.** The dedup engine keeps state: `chromemStore` (in-RAM vector index), memdb (book/file records, stripped of fingerprints). Estimate RSS contribution from each. Are there obvious reductions?

6. **GC pressure.** The cache warm-up pattern (storing full API response objects) is the known root cause. Are there other similar patterns in the codebase where large objects are cached unnecessarily?

### Spec Requirements

1. For each optimization with >10% estimated impact: a concrete migration plan with before/after key schema, data migration steps, rollback path
2. Prioritized list (effort vs. savings estimate)
3. Any optimizations that require a production maintenance window vs. those that can be hot-deployed

---

## Priority 4: General Code Review, Security, and Finish-Line Items

### Review Scope

Do a thorough review of the following areas. For each finding, classify: SECURITY / BUG / PERFORMANCE / DEBT, and CRITICAL / HIGH / MEDIUM / LOW severity.

1. **Security:**
   - Path traversal / injection in file operations (`internal/fileops/`, `internal/scanner/`, path handling generally)
   - Auth middleware — API key handling, invite flow (`internal/auth/`)
   - Input validation at HTTP layer — are all endpoints validated? Request body size limits?
   - SSRF — any outbound HTTP calls that take user-supplied URLs?
   - The known open finding: `POST /api/v1/auth/accept-invite` returns `{"error":"EOF"}` under HTTP/2 (per pen test June 4, 2026); root cause not yet confirmed

2. **iTunes writeback security:**
   - Are there any path traversal opportunities in the path mapping/repair logic?
   - Is the PID-to-path mapping sanitized before writing to the .itl?

3. **Concurrency bugs:**
   - The dedup engine has background goroutines (`bgCtx`, `bgMu`). Is the lifecycle correct (PostInit / Stop)?
   - `Registry.Shutdown` nil-cancel race was fixed in PR #1239 — are there similar patterns elsewhere?

4. **Test coverage gaps:**
   - 31 narrow test-coverage burndown tasks (#79–#109) are queued. Review `make ci` output and identify the highest-value gaps that aren't already assigned.

5. **Finish-line items:**
   - `BookFile.AcoustIDFingerprint` whole-file migration (Step 3 LSH index — on hold)
   - `DedupCandidate` false-positive 14K 100% matches from pre-whole-file fingerprints — root cause and fix
   - Memory leak: cache warm-up disabled but root cause (large object caching) not fixed
   - Duration/filesize aggregation: Book fields show snapshots instead of sums from BookFiles
   - `filterUnchangedTags` includes all custom tag fields for skip detection — is this correct and tested?
   - Single-file books need virtual segment for rename to work — is this implemented and tested?

---

## Implementation Plan Requirements

After completing the analysis and specs, produce an **Implementation Plan** with these properties:

### Task Format

Each task must have:
```
TASK-{N}: {Title}
Priority: P1/P2/P3/P4 (maps to the 4 priorities above)
Effort: S/M/L (S=<2h, M=2-8h, L=>8h)
Agent: sonnet-4.6
Depends: [list of TASK-{N} that must complete first]

## Context
[1-3 paragraphs: what exists now, what's wrong, why this fix matters]

## Exact Files to Change
- `path/to/file.go` — what changes and why
- `path/to/file_test.go` — what tests to add/modify

## Step-by-Step Instructions
1. [Specific, unambiguous step — names exact functions, line ranges if known]
2. ...
(≥5 steps, enough that a Sonnet agent with no prior context can execute correctly)

## Acceptance Criteria
- [ ] Specific, testable criterion
- [ ] Another criterion
(All criteria must be verifiable by running `make ci` or a specific test command)

## Idempotency Notes
[How to tell if the task has already been done; what to check before starting; what to do if interrupted halfway]

## Rollback
[How to undo this change if it breaks something in production]
```

### Ordering Principles

- Tasks must be ordered so dependent tasks come after their dependencies
- Tasks that modify the same file must be serialized (cannot run in parallel)
- Each task should touch at most 5-7 files (split larger changes)
- Database migrations (PebbleDB key format changes) are always separate tasks with explicit backfill steps
- Frontend + backend tasks for the same feature can be parallel if they share only an API contract
- Test tasks can be parallel if they're in different packages

### Agent Coordination Model

After the implementation plan is approved, the workflow will be:
1. Fable 5 (you) coordinates and validates
2. Sonnet subagents execute tasks
3. Each Sonnet agent returns a PR diff for Fable 5 to review before merge
4. Fable 5 flags any issues, requests changes, or approves

Design the task breakdown with this coordination model in mind. Flag tasks where Fable 5 review is especially critical (e.g., iTunes writeback changes, PebbleDB schema changes).

---

## Output Structure

Produce your output in this order:

```
# FINDINGS: Security & Bugs
## CRITICAL
## HIGH  
## MEDIUM
## LOW

# SPEC 1: Unified Dedup Pipeline
## Current State Analysis
## Architecture Design
## Data Model
## API Changes
## UI Design
## LSH Implementation
## Scoring Formula
## Migration Plan

# SPEC 2: iTunes Writeback Hardening
## Damaged Library Analysis
## Safety Contract (ITLSafetyContract)
## Atomic Write Protocol
## Apple Devices Compatibility Checklist
## Safeguard Gaps & Implementations
## Regression Test Suite Design

# SPEC 3: Memory & Database Optimization
## PebbleDB Schema Audit
## SQLite Elimination Plan
## Memory Sizing Estimates
## Prioritized Optimization List

# IMPLEMENTATION PLAN
## Dependency Graph (Mermaid or ASCII)
## TASK-001 through TASK-N (full format for each)
## Parallel Execution Groups (which tasks can run simultaneously)
```

---

## Completion Report (required format)

When you finish, your chat summary must give **exact counts**, never "all done" / "complete" without a number backing it. End with these three lines (this repo's mandated format):

```
COMPLETED: <count> — <list of specs + plan files written, with paths>
REMAINING: <count> — <anything specced but not finished, or analysis you couldn't complete>
BLOCKED: <count> — <anything you couldn't do and why, e.g. missing /tmp evidence>
```

Also report, specifically: N specs written, N tasks in the implementation plan, and findings counted by severity (CRITICAL/HIGH/MEDIUM/LOW). If a precondition failed (e.g. damaged libraries missing), that goes under BLOCKED — do not silently skip it.

## Final Notes

- The owner is opinionated about **not using SQLite** — every recommendation that adds a SQLite dependency will need extra justification
- **Production is Linux** — don't suggest macOS-only tools or patterns
- **This is a personal project** — the owner cares about correctness and UX, not about enterprise scale or regulatory compliance
- The iTunes integration is the **highest-stakes** area: a corrupt library can destroy years of carefully curated playlists and playback positions. Treat every iTunes change as potentially irreversible.
- The dedup pipeline is **the feature** that makes this project worthwhile — getting it to ~98% accuracy is the goal that unlocks actually using the system day-to-day
- Comments in code and spec should explain the WHY (constraints, invariants, surprises), not restate the WHAT
