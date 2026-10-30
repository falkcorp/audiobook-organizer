<!-- file: docs/decisions/2026-10-30-min-score-slider-findings.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7d3c1a52-0e84-4b6f-9a17-c5e2b8f40d63 -->
<!-- last-edited: 2026-10-30 -->

# Minimum match score slider: nothing at 150, 40 rows at 200

Owner report 2026-10-29: in the metadata review queue, the "Min match score"
slider at 150 lists nothing, at 200 it lists 40 books.

## What the frontend does (checked, not the cause)

- The slider (`QueueRail.tsx`, max 300) writes `filters.confidenceThreshold`,
  in score x 100 units, the same unit the server reads.
- `reviewQueryFor` sends it as `min_confidence` when above 0;
  `reviewQueryString` sends it unchanged. No second param, no unit change.
- The request key is `JSON.stringify` of the whole query, so 150 and 200 are
  different keys. The 150 ms debounce aborts and re-issues; it does not drop
  the value.
- The only client use of the threshold besides the request is
  `highConfidenceIds` (a bulk-select helper). No client filter is applied on
  top of the server page.

## What the server does (the cause)

`evaluateReviewFilters` applies the minimum score per row, which is monotonic.
The next step, `hide_multi_book`, then removes every book that shares a
candidate with another book, counted over the rows that survived the score
filter. That step is not monotonic in the minimum:

- Books A (score 1.95) and B (1.60) share one candidate.
- At 190 only A survives, so A is alone and listed.
- At 150 both survive, form a group, and both are hidden.

The review levels turn `hide_multi_book` on by default (Normal and above), so
the report happens with default settings. The earlier check that a lower
minimum returns a superset held for the score filter alone.

## Fix

Multi-book groups are counted in the same single pass that filters, over the
SCOPE set: the rows that source, row state, no-match and the Title keep,
before any threshold. The thresholds are the minimum score, `hide_runtime`,
the language switch and the transcription filters. The group hide is then
applied to the rows that pass every filter. A book a threshold removes still
counts as a group member, so grouped books stay hidden at every setting and
loosening any threshold lists a superset. `runtimeHidden` and the other
counters are still counted on the real filtered pass.

The same defect shape was in `hide_runtime`, the language switch and the two
transcription filters (all ran before grouping); the first commit fixed only
the minimum score, this one fixes all of them.

Tests: `TestReviewQuery_ThresholdsMonotonicWithHideMultiBook` in
`internal/server/handlers/metadata_cache_query_test.go` loosens each of
min_confidence, hide_runtime, has_transcription and transcription_matched and
requires a superset. Against the original code four of its five steps fail.

## Decision log

- Chosen: count groups over the scope set (everything but the thresholds),
  once, in the filter pass. Rejected: a second `filterRows(min=0)` call for the
  score only (fixes one filter, leaves runtime and transcription); counting
  over every row with no filter at all (breaks the pinned shared case
  "multi-book is computed over the filtered set", where a group partner the
  Title filtered out must not group); dropping the multi-book hide when a
  minimum is set (changes what the switch means); moving the hide to the
  client (the server owns paging and totals).
- The Title, source and row-state filters narrow the scope on purpose, so
  they can still change which books group. Only the thresholds are fixed to
  be monotonic.
- No Vitest test was added: the frontend sends the value correctly, so a client
  test would pass without any change. The Go test fails without the fix.
- Not confirmed against prod: the API key in `.claude/.api-token` has expired
  (HTTP 401), and refreshing it restarts the service. Compare 150 vs 200 on
  prod after deploy. If 150 still lists nothing, the cause is another
  non-monotonic filter (`hide_runtime`, transcription) or the data, and this
  note is incomplete.
