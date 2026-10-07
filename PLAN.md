<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0e418856-2dc7-414a-8b2a-43f31e164235 -->
<!-- last-edited: 2026-10-06 -->

# Review → Metadata: always-visible select-all, chip + title regex, score label

## Goal

1. The owner could not find "select all": it only appeared after ticking a
   hidden per-page checkbox. Put an always-visible selection bar (Select page /
   Select all N matching / Clear / X selected) at the top of the main pane and
   in the queue header.
2. Summary chips ignored the Title filter regex. Make chip views honour it (other
   filters stay paused), prune selections the new regex excludes, and show an
   invalid regex as a visible error.
3. "Min confidence: 190%" is wrong on both words. Candidate scores are additive
   evidence sums, so relabel it as a unitless "match score" everywhere the
   review lane prints `score * 100`.

## Files

- `web/src/components/review/SelectionBar.tsx` (new)
- `web/src/components/review/MetadataPanel.tsx`: sticky bar above the spine
- `web/src/components/review/QueueRail.tsx`: bar in the queue header, chip
  banner text, Title filter error, score label, row score chip
- `web/src/components/review/lanes/useMetadataLane.ts`: chip rows honour
  `titleRegex`, `titleFilterError`, chip-mode title prune
- `web/src/components/review/spine/CompareSpine.tsx`: unitless score chips
- tests: `ReviewWorkspace.selectAll.test.tsx`, `ReviewWorkspace.chipFilters.test.tsx`,
  `spine/CompareSpine.test.tsx`, new `SelectionBar.test.tsx`
- `changelog.d/20261006_review_metadata_selectall.md`

`ReviewWorkspace.tsx` needs no change: it already passes `viewMode` through to
MetadataPanel, and the bar sits outside CompareSpine, so it renders the same in
every view mode.

## Steps

1. Lane: `titleFilterError`, apply `titleRegex` to `chipRows`, add the chip-mode
   title prune.
2. SelectionBar component; use it in MetadataPanel (sticky) and in the QueueRail
   header, replacing the conditional checkbox and banner.
3. QueueRail: banner text, TextField error/helperText, score label + tooltip,
   level description text, row chip.
4. CompareSpine: drop `%` from the 4 score chips.
5. Tests + changelog fragment.

## Test strategy

Vitest: bar visible in compact / two-column / auto, and in a chip view with 0
rows (buttons disabled); select page / select all matching / clear; chip + title
regex; chip-mode prune (renderHook); invalid regex error; score label text.
Red/green: revert the lane hunk, watch the chip+regex and prune tests fail, then
restore it. `npx vitest run src/components/review` and `npx tsc --noEmit -p web`.

## Rollback

Revert the single commit. There is no stored-state or server change: the
filter value, NORMAL_PRESET and the localStorage keys are unchanged.

## Decisions for owner to validate

1. **"Select page" is a toggle.** When every row on the page is already selected
   it reads "Deselect page (N)" and unticks them. The old header checkbox could
   untick a page, and a select-only button would have lost that.
2. **The bar shows in two places**: sticky at the top of the main pane, and in
   the queue rail header. Both call the same lane functions. The rail copy keeps
   the old `select-page` / `select-all-matching` test ids and the main copy uses
   `main-` prefixed ids, because ActionBar already owns `selected-count` and
   `clear-selection`.
3. **"Select page" means the whole page (`pageResults`)**, including books the
   spine draws inside a multi-book group, which is what the old checkbox did.
4. **The old "All 25 on this page selected → Select all N" banner is gone.**
   "Select all N matching" is always visible and is disabled once all N are
   selected, and the live "X selected" count replaces the banner text.
5. **An invalid regex filters nothing.** The rows stay as they were and a red
   helper text explains the error. The alternative, showing zero rows, looks like
   "no matches", which is the silent failure the owner objected to.
6. **Chip-mode prune drops only selections whose title is known and fails the new
   regex.** Ids missing from the loaded rows are kept, and so are selections that
   no regex is set to exclude. The prune is skipped while the regex is invalid or
   empty, so typing a half-finished pattern never wipes a selection.
7. **The regex is still JavaScript `RegExp` (case-insensitive), not RE2.** The
   owner ruled on 2026-10-06 that the Title filter and the library search should
   share RE2. JS accepts lookahead and backreferences, which RE2 rejects, so this
   error check under-reports against that target. Unifying the syntax
   (server-side RE2) is separate work and is not done here.
8. **The score number is `Math.round(score * 100)` with no unit**, and the same
   number appears in the rail label, the row chip and the spine chips.
   `EvidencePanel`'s dedup "confidence %" is left as is: those values are
   per-signal probabilities capped at 100, so `%` is correct there.
9. The spine chip's colour thresholds (0.85 / 0.6) are unchanged. Only the text
   changed.
