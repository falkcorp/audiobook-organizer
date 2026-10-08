<!-- file: docs/plans/2026-10-07-review-candidates-view.md -->
<!-- version: 1.0.0 -->
<!-- guid: 670533c3-77d8-45f5-ac86-f116f136e60f -->
<!-- last-edited: 2026-10-07 -->

# Plan: Review → Metadata "Candidates" view, full book info on every card, real no-candidate labels

Owner-approved 2026-10-07 20:22. Branch `feat/review-metadata-card-info-and-fetch`.

## Goal

1. The third view toggle (`auto`) becomes **Candidates**: per card, full book info on the
   left and the full ranked search-candidate list (the Search Metadata dialog's list) on
   the right, with per-candidate Apply / Reject, a collapsible "How this score was
   reached", and a per-book "Search again" (editable title/author).
2. Every view's card shows the same book info block.
3. The 18,881 "Error: Unknown" chips show what the row actually is.

## Findings that shape the design

- **"Error: Unknown" is a frontend mapping bug.** `TwoColumnCard` (used by two-column and
  auto) labels every candidate-less row that is not `no_match` as
  `Error: ${error_message || 'Unknown'}`. The unreviewable bucket serves
  `no_candidates` / `resolved_no_candidates` rows that are not errors and carry no
  `error_message`; the server sets `errMsg` only for `decode_error`
  (`metadata_cache.go`). `CompactRow` already tells these apart. Fix: one shared
  status-label helper used by every renderer; `decode_error` shows the server's text.
- `viewMode` is plain `useState('compact')` in `ReviewWorkspace`, never persisted, so there
  is no stored `'auto'` to migrate. `DupesSpine` only checks `=== 'two-column'`; it will
  treat `'candidates'` as two-column.
- `POST /audiobooks/:id/search-metadata` returns `MetadataCandidate`s, which carry
  `score_breakdown`; `EvidencePanel` already handles a missing one.
- The per-book apply endpoint (`POST /audiobooks/:id/apply-metadata`, `background: true`)
  has no fill/replace mode, only `fields`. **Choice:** compute fill fields client-side from
  a fresh `api.getBook` at apply time (fields whose current value is empty and the
  candidate has one; fields the Book payload cannot show are left out in fill mode, never
  overwritten). Replace sends every field. No new endpoint, no server change.
- There is no candidate-level reject endpoint. Reject on a candidate that is the cached
  top candidate dispatches the lane's existing per-book `reject`; Reject on any other
  candidate hides it locally for the session. Noted as a limitation.

## Files

- `internal/metabatch/candidates.go` (+ test): `CandidateBookInfo` gains `narrator`,
  `series`, `series_position`, `asin`, `isbn`, `file_count` (from `BookFileFacts`; absent on
  the no-files builder). No file list in the list response (N+1 / payload); the card
  lazy-loads `GET /audiobooks/:id/files?disk_check=false` on expand.
- `web/src/services/api.ts`: the new `CandidateBookInfo` fields.
- `web/src/components/review/spine/BookInfoPanel.tsx` (new): shared left block.
- `web/src/components/review/spine/rowState.ts`: `noCandidateLabel` helper.
- `web/src/components/review/spine/CandidatesCard.tsx` (new) + `candidateLoader.ts` (new):
  candidates view and the cap-4 lazy loader / cap-4 apply limiter.
- `CompareSpine.tsx`, `ReviewWorkspace.tsx`, `MetadataPanel.tsx`: mode rename, wiring.

## Loading and apply rules

- The row's cached top candidate shows immediately; the full list is fetched when the
  card scrolls into view (IntersectionObserver), through ONE spine-scoped queue with at
  most 4 requests in flight. A card that leaves the viewport before its turn is dequeued.
  Results are memoized per book + query so scrolling back does not refetch.
- Applies use `submitStagedApply` (the dialog's background path, ASIN-conflict aware), one
  op per book, at most 4 in flight; completion triggers the panel's debounced refresh.

## Tests

- vitest: candidate list renders; Search again sends the edited title/author; lazy-load cap
  of 4 (10 visible cards, deferred promises); Apply calls `applyMetadataCandidate` with
  `background: true` and fill-mode fields; no-candidate labels; existing auto-mode tests
  updated to candidates.
- Go: `bookRowInfo` / facts builder fields.
- `npm run build --prefix web`, vitest on touched files, `go build ./... && go vet` on
  `internal/metabatch`.

## Rollback

Frontend-only behaviour plus additive JSON fields; revert the commits.
