<!-- file: docs/executive-summaries/2026-10-19-review-full-index-retired-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b0c1e52-93f4-4d7a-8e21-5a7c3f9d4b60 -->
<!-- last-edited: 2026-10-19 -->

# The Review page can no longer load the whole library into your browser

Branch `task/02-pr19`, PR not yet merged. Spec: `docs/proposals/2026-10-holistic/tasks/02/02-PR19.md`, the last step of the Review > Metadata move to the server.

## Executive Summary

- **The old way of loading the Review page is gone.** Until now the Metadata tab could
  fetch every book waiting for review in one very large download and sort, filter and count
  them inside the browser. That is what let one Review tab grow to many gigabytes. The tab
  now asks the server for one screenful at a time, together with the counts for the
  left-hand chips, and it has been doing that in production since 2026-10-18 with no
  review errors.
- **The switch that chose between the two ways is removed.** The setting
  `review_metadata_server_query` no longer exists. A saved settings file or database that
  still contains it keeps loading, a note is logged that it is ignored, and a save of the
  settings file drops it.
- **Anything still asking for the old download gets a clear refusal.** The old request now
  answers "410 Gone" and names the replacement. Each such request is counted on the review
  latency dashboard under the label `gone`, so a forgotten client would show up.
- **What did not change.** Nothing is written by either way of loading, no book data was
  touched, and applying, rejecting, skipping and searching again behave as before.
- **A dashboard panel needs a decision.** The "review index p95" panel reads the label
  `index`, which no longer receives data, and the screenful requests are not timed at all.
  Whether to time them is an open choice, filed in the to-do list.

## Why it is safe to do now

The usual rule is to wait for one full release with the switch on. The owner waived that on
2026-10-19 ("Go ahead now"): the switch has been on in production since 2026-10-18 16:05,
the server side (02-PR18) is in v0.222.2, and no full release has gone out since the switch
was turned on.
