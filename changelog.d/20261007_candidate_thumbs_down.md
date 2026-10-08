### Added

#### Review → Candidates: "Not the best match" thumbs-down, kept as scorer training data

Every candidate row in the Candidates view now has a thumbs-down button
(tooltip "Not the best match"). It does not reject the book, skip it, or hide
the candidate. It records a labelled negative example and marks the row; a
second click undoes it. When an Apply lands, the applied candidate is recorded
as the positive for the same book and query. Each label stores the book, the
query (typed title/author, browse vs per-book), the candidate's identity and
compared fields, its score and score breakdown as shown, its rank, the user,
and timestamps. The candidate also carries the same source hash an apply
stamps as the book's `MetadataSourceHash`, so the labels join against the
ground truth `metafetch.calibrate-scoring` already replays.

Storage is a new Pebble key family `candfb:` (`internal/database/candidate_feedback.go`),
one record per book + query + candidate with the label in the value, so a
thumbs-down followed by an Apply of the same candidate becomes a positive
rather than two contradictory rows. Endpoints (metadata edit permission):
`POST /api/v1/metadata/candidate-feedback`, `DELETE
/api/v1/metadata/candidate-feedback/:id?label=` (removes only while the record
still carries that label), and `GET /api/v1/metadata/candidate-feedback`
(JSON Lines export, `?book_id=` filter, paged so memory does not grow with the
dataset).
