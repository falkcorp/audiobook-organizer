<!-- file: docs/specs/2026-06-19-gold-labels-review-ui-and-path-abbrev-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c8e1d57-9a42-4b6f-bd03-7e15a9c2f640 -->
<!-- last-edited: 2026-06-19 -->

# Gold Labels Review UI + Path Abbreviation — Design

## Goal

Bring the dedup **Gold Labels** review page (`/dedup/labels`,
`web/src/pages/DedupLabels.tsx`, backend
`internal/server/handlers/dedup/label_review.go`) up to the visual/quality bar set
by `CandidateCompareDrawer`, and clear the documented dedup follow-ups. The work is
split into four independent tracks, each shipping as its own PR (or several small
PRs). More, smaller PRs are preferred over fewer large ones.

The visual/quality reference for all UI work is
`web/src/components/dedup/CandidateCompareDrawer.tsx` (the dedup picker with the
fingerprint tab).

## Background / current state

- Each gold-label row carries a `candidate_id`. The existing endpoint
  `GET /api/v1/dedup/candidates/:id/breakdown` returns full `book_a` / `book_b`
  detail plus `score_breakdown.signals` — the same payload `CandidateCompareDrawer`
  already renders. Req #3 (metadata-compare) is therefore largely a *reuse* of
  existing infrastructure rather than new plumbing.
- Path display today is ad-hoc: `FileInfoCompare.tsx` has a local `truncatePath`
  that strips at the literal marker `audiobook-organizer/`; there is an unused
  `filesCommonDir` helper at `internal/server/server_middleware.go:63`.
- The dedup gold dataset on prod has ~5,946 labeled examples (1,153 true_dup,
  4,092 not_dup, 701 unlabeled, 248 auto_high_conf). The page is live but needs the
  fixes below.

## Decisions (confirmed with user)

1. **Compare UX**: reuse `CandidateCompareDrawer` (drawer now, inline expansion
   considered later only if row-by-row review feels slow).
2. **Label control**: 3-way segmented toggle `[ Dup | Unsure | Not ]`.
3. **PR scope**: four separate tracks (A–D), each its own PR, split into smaller
   PRs as convenient.
4. **`books` variable**: derived as `filepath.Dir(RootDir)` — no new config field.
5. **Tracks B and C are split** even though both touch the gold-labels page
   (B = label control + path display + clickable rows; C = drawer integration +
   extra judging fields).

---

## Track A — Path abbreviation helper (foundational, app-wide)

### Variables (most-specific first)

| Var          | Value                                              | Example render                              |
|--------------|----------------------------------------------------|---------------------------------------------|
| `$(libroot)` | `config.AppConfig.RootDir` (`/mnt/bigdata/books/audiobook-organizer`) | `$(libroot)/Sanderson/Mistborn/01.m4b` |
| `$(books)`   | `filepath.Dir(RootDir)` (`/mnt/bigdata/books`)     | `$(books)/itunes/iTunes Media/x.m4a`        |
| (none)       | anything else                                       | full path, or common-prefix-stripped        |

Checked `libroot` first so the more-specific prefix wins; a path under `libroot` is
never rendered as `$(books)/audiobook-organizer/...`.

The abbreviation token (`$(libroot)`, `$(books)`) is rendered **literally** as a
visible signal that the path is abbreviated. In React this is plain string text; in
any markdown/terminal context the `$(...)` is escaped so it prints verbatim.

### Backend

New package `internal/pathutil/abbreviate.go`:

```go
// AbbreviatePath returns p with the most-specific known root replaced by a
// literal $(var) token. Roots are checked libroot-first.
func AbbreviatePath(p string) string

// PathVars returns the {name, value} pairs in match order, for the UI legend
// and for the frontend formatter to stay in sync with the backend.
func PathVars() []PathVar
```

- Roots are read from `config.AppConfig.RootDir` at call time (handles hot-reload).
- The unused `filesCommonDir` logic folds in here as the "strip a sensible common
  prefix" fallback for multi-file books, becoming a real call site instead of dead
  code. The old `filesCommonDir` is removed (or made a thin wrapper) and its one
  intended use is rewired to the new helper.

### Frontend

`web/src/utils/formatPath.ts`:

- `formatPath(path: string, vars: PathVar[]): string` — mirrors the backend rules.
- `usePathVars()` — reads `{libroot, books}` once from the existing config
  (`GET /config` exposes `RootDir`; `books` derived as the parent) and caches it, so
  there is a single source of truth shared with the backend ordering.
- `<PathVarsLegend />` — renders the footer:
  `Information: $(libroot) = /mnt/bigdata/books/audiobook-organizer · $(books) = /mnt/bigdata/books`.

Replaces the local `truncatePath` in `FileInfoCompare.tsx` and is then spread to the
highest-traffic path displays (file lists, book detail, dedup tabs) incrementally.

### Naming

`libroot` and `books` — short and clear. `libroot` over `library-root`; `books`
over `booksdir`.

### Test strategy (A)

- Go table tests for `AbbreviatePath`: libroot match, books match, libroot-wins-over-books,
  unrelated path, trailing-slash, exact-root, empty.
- Vitest for `formatPath`: same cases, parity with Go expectations.

---

## Track B — Gold Labels: real label control + paths + clickable rows (req #2)

- Replace the two text buttons (`dup` / `not`) with an MUI `ToggleButtonGroup`:
  **[ Dup | Unsure | Not ]**, current label highlighted (green / amber / red).
  Setting a value calls the existing override endpoint (which already writes
  `label_source=human`).
- File name + abbreviated path via the Track-A `formatPath`; **hover shows the full
  path** (Tooltip), preserving current behavior.
- Title / file cells become **clickable → open the book**: opens the Track-C drawer
  (preferred) or navigates to `/library/:id` as a fallback before C lands.
- Tighten table density toward the `CandidateCompareDrawer` bar.

### Test strategy (B)

- Vitest: toggle renders current label as selected; clicking a value POSTs the
  override and optimistically updates; tooltip carries full path; row click fires the
  open handler.

---

## Track C — Gold Labels: metadata-compare drawer (req #3)

- Row click opens the existing `CandidateCompareDrawer` keyed by `candidate_id`
  (it already fetches `/breakdown` → files + score breakdown + fingerprint +
  open-book).
- Extend the compare with the extra judging fields: **series, narrator,
  parts/file-count, total duration, total size, and which signal fired**
  (`score_breakdown.signals[].kind / evidence`, `primary` flag). Most exist in the
  breakdown payload already; surface the missing ones (series / narrator) by widening
  `DedupBookDetail` + the breakdown handler. Because the drawer is shared, the dedup
  picker gains the richer fields too.

### Test strategy (C)

- Go: breakdown handler returns the widened fields (series_name, narrator_name).
- Vitest: drawer opens from a gold-labels row; new fields render; existing
  CandidateCompareDrawer tests stay green.

---

## Track D — Backend dedup fixes (req #4), each its own PR

- **D1 — `quarantine-chapter-artifacts` root cause.** Dry-run currently finds only
  53 books and misses the unscanned (`Duration=0`) "Opening Credits" / "Big Finish
  Ident" idents despite the v2 unscanned-branch. Instrument the dry-run to log *why*
  each candidate book is skipped; confirm whether the cause is normalized-title
  bucketing or `GetBookFiles` via memdb returning ≠1 for `Duration=0` idents; fix.
  **Dry-run only — the list is shown to the user before any apply.**
- **D2 — Scanner prevention.** `groupFilesIntoBooks` (`internal/scanner/scanner.go`)
  stops emitting standalone books for single-file short segments that sit alongside a
  multi-file book — the real upstream cause of the candidate balloon.
- **D3 — Drain stale exact candidates.** Once D1/D2 criteria are right, drain the
  remaining ~356K stale exact candidates.
- **D4 — Manual-import UI button.** Frontend for the already-merged+deployed
  `library.import` op (folder / file picker).

### Test strategy (D)

- D1: unit test that an unscanned single-file ident with a colliding title is
  selected by the dry-run; regression for the previously-missed branch.
- D2: scanner test that a single-file short segment beside a multi-file book is not
  emitted as a standalone book.
- D4: Vitest for the import button + picker; backend already tested.

---

## Rollback

- Tracks A–C are additive UI/helper changes; rollback = revert the PR. The path
  helper has no data effects.
- Track D1 is dry-run only (no writes). D2 changes scan output going forward only
  (no migration). D3 is the only data-mutating step and is gated behind explicit
  user confirmation with the list shown first.

## Build order

A → B → C → D1 → D2 → D3 → D4. A is foundational (B/C consume it). D items are
independent of A–C and can interleave, but D3 depends on D1+D2.
