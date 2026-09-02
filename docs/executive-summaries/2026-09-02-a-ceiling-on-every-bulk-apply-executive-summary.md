<!-- file: docs/executive-summaries/2026-09-02-a-ceiling-on-every-bulk-apply-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e3b7a20-4c15-4f8d-b6e2-71d0a8c3f5b4 -->
<!-- last-edited: 2026-09-02 -->

# A ceiling on every bulk apply

**Pull request:** feat/bulk-apply-cap (link added at merge)

## Executive Summary

- Several past incidents in this app had the same shape: a request meant to change a
  handful of books lost its filter somewhere along the way and quietly changed the whole
  library instead. Each time the bug itself was different; the damage was the same.
- This change adds one hard rule underneath all of those paths: no single "apply" may
  touch more than 5,000 items. Ten places that apply a list of changes at once now count
  the list first and, if it is too long, stop before writing anything. The request fails
  loudly with a message that names the count, the ceiling, and the setting that raises it.
- It is a refusal, not a trim. A request for 5,001 items does not do the first 5,000 — it
  does zero, so a broken filter can never be half-applied and then hard to notice.
- The ceiling is a setting (`bulk_apply_max_items`). Setting it to zero means "use the
  default", never "no limit" — a zero has been a silent off-switch in this app before, and
  this setting cannot be one.
- Preview ("dry run") modes are not limited, and the review replay preview now tells you
  the ceiling, so you can size a real run under it.
- Every one of the ten gates was tested both ways — one over the ceiling is refused with
  nothing written, exactly the ceiling goes through — and each test was then checked by
  deliberately removing or loosening its gate and confirming the test fails. Sixteen of
  sixteen such checks were caught.
- Deliberately left for follow-up PRs, each needing its own gate and test: writing tags
  into audio files in bulk, the metadata import, the AI author-merge, and the step that
  saves a scan's results.

## Why a ceiling and not just fixing the bugs

The individual bugs were fixed as they were found. What was missing was a control that
holds even for the bug not found yet. Nothing legitimate in this library applies 5,000
changes in one request; anything that tries is, by definition, a mistake. The ceiling turns
"the whole library changed overnight" into "a request failed with a clear message".
