### Changed

#### The activity index pushdown now refuses an unrecognised filter instead of guessing

The gate that decides whether an activity query can be served from the
`act:op:`/`act:bk:` secondary index (`pactIndexPushdownEligible`) was a
deny-list: it enumerated the filter fields it knew could not be decided from an
index key and refused those. Anything it did not recognise fell through and was
accepted.

That is the wrong direction to fail. A field added to `ActivityFilter` later
would have been pushed down silently — the pushdown decodes only the requested
page, so a predicate it cannot evaluate inflates the `total` it reports without
returning a single wrong row. `total` drives the UI's pager and has no error
channel, so the symptom would have been a plausible wrong page count and nothing
else.

The gate is now an allow-list (`pactPushdownDecidable`) walked reflectively over
every field of `ActivityFilter`. A field that is not explicitly classified as
decidable from the index key refuses the pushdown, so the failure mode for an
unclassified field flips from "wrong count" to "loses the fast path" — the same
answer, computed the slow way. A renamed field misses the lookup and refuses
too.

Classification is unchanged for all fifteen fields that exist today, including
the deliberate one: `Since` and `Until` are still accepted, and still only
because *neither* implementation honours them. That remains a separate known
defect (`GET /api/v1/activity?operation_id=X&since=…` ignores `since`), and
whoever fixes it must remove those two entries from the allow-list or the two
paths will diverge silently on every time-bounded request.
