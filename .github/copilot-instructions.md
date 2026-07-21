<!-- file: .github/copilot-instructions.md -->
<!-- version: 4.2.0 -->
<!-- guid: 4d5e6f7a-8b9c-0d1e-2f3a-4b5c6d7e8f9a -->
<!-- last-edited: 2026-07-21 -->

# Audiobook Organizer — Additional Copilot Context

Org-wide coding standards (file headers, Go/TS rules, commit format) are at
**https://github.com/falkcorp/.github** and apply automatically to this repo.

This file contains audiobook-organizer-specific additions only.
For full project context see **CLAUDE.md** at the repo root.

## Project overview

Go 1.24 backend (Gin) + React 18/TypeScript frontend (Material UI).
DB: PebbleDB (primary), NutsDB (activity log). SQLite removed Jun 2026.
Integration: Open Library, AcoustID fingerprinting, OpenAI batch API.

## Key directories

| Directory | Contents |
|---|---|
| `cmd/` | CLI and server entry points |
| `internal/` | Go backend packages |
| `web/src/` | React frontend |
| `docs/specs/` | Design specs |
| `docs/plans/` | Implementation plans |
| `.github/prompts/` | AI agent prompts |

## Critical constraints

- `UpdateBook` does **full column replacement** — always supply all fields.
- iTunes XML path must **never** be scanned by the file scanner.
- Production is Linux (`make deploy`). Do not use macOS-specific commands.
- Worktree discipline: never commit directly to `main`. All changes via PRs.

## Dedup architecture

### Candidate emission gate (`hasPlausibleAudio`)

`internal/dedup/engine.go` — `hasPlausibleAudio(book *database.Book) bool`
returns true when `Duration > 0` OR `FileSize >= 256 KiB` (256 × 1024 bytes).

The `checkExactTitle` and `checkExactISBN` emitters call this gate for **both
sides** of a pair before emitting a `DedupCandidate`. This prevents stub or
unscanned books (zero duration, zero file size) from being emitted as candidates
and flooding the review queue with false positives.

The AcoustID collector `CollectExactAcoustID` (`internal/dedup/collectors_acoustid.go`)
is **intentionally NOT gated** — an AcoustID match is itself sufficient evidence of
audio content.

### Labeled training dataset

The dataset subsystem lives in two packages:

- **`internal/database/dedup_label.go`** — PebbleDB keyspace `dedup:label:`.
  Stores `LabeledExample` records: candidate pair + per-book feature snapshots
  (`BookFeatures`) + label fields (`label`, `label_source`, `label_reason`,
  `decided_at`). Empty `label` means unlabeled (features only, for future ML).

- **`internal/dedup/dataset/`** — pure package; no DB side effects.
  - `BuildExample` computes: duration ratio, folder relation (unrelated /
    same_dir / a_ancestor_of_b / b_ancestor_of_a), recording-ID overlap,
    whole-book signature relation (unknown / match / disjoint).
  - `Classify` runs three deterministic catchers in priority order:
    1. `wholeBookSignatureMatch` → `true_dup` (positive oracle; similarity ≥ 0.95)
    2. `missingFile` → `not_dup` (never merge a book with no files)
    3. `partVsWhole` → `not_dup` (duration ratio < 0.5 with both durations known)

### Backfill op

`internal/plugins/dedup/dataset_backfill.go` — `dedup.dataset-backfill` UOS op.
Dry-run by default; `apply=true` writes labeled examples and dismisses
rule-confirmed `not_dup` candidates (status → `dismissed`). Idempotent.

Known gap: pairs where one side has `Duration=0` but file records exist are NOT
caught by the current catchers; they remain unlabeled. The engine gate stops NEW
such pairs; a future FileSize-aware catcher will handle the existing residual.


## 📝 Changelog & TODO — Use the Fragment System (MANDATORY)

**Do not hand-edit `CHANGELOG.md`, and do not add new tasks straight into the
`TODO.md` inbox.** Both files are assembled from per-change fragments so that
parallel PRs never collide on them.

- **`CHANGELOG.md` is assembled, not hand-edited.** Add a fragment under
  `changelog.d/` (run `scriv create`, or write the Markdown file by hand). The
  fragments are folded into `CHANGELOG.md` at release time by `scriv`, and a CI
  check (`changelog-check.yml`) requires one on each PR. See `changelog.d/README.md`.
- **New `TODO.md` tasks are added via fragments.** Drop a Markdown fragment in
  `todo.d/` (see `todo.d/README.md`) instead of editing the `## 📥 Inbox`
  section. `scripts/assemble_todo.py` folds fragments in daily. This is
  **add-only**: checking a task off or removing it is a normal direct edit of
  `TODO.md`.
