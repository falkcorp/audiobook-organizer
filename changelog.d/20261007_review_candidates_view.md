### Added

#### Review → Metadata: Candidates view

The third layout toggle (was "Auto layout") is now **Candidates**: each card shows
the book's full info on the left and, on the right, the full ranked candidate
list the per-book Search Metadata dialog shows (same endpoint), each with Apply,
Reject and a collapsible "How this score was reached", plus a per-book
"Search again" with editable title and author. The cached pick shows at once;
the full list loads only for cards on screen, at most 4 searches at a time.
Applies use the dialog's per-book background apply (at most 4 at a time) and
respect the page's Fill empty fields / Replace existing toggle. Rejecting a
candidate other than the cached pick hides it for the session only (there is no
candidate-level reject on the server).

#### Book info on every review card

Every metadata review view now shows narrator, series, ASIN/ISBN, runtime, size,
file count with an expandable file list, and paths. The review list response
gains `narrator`, `series`, `series_position`, `asin`, `isbn` and `file_count`
on each row's `book`.

### Fixed

#### "Error: Unknown" on review rows that have no candidate

The two-column card labelled every candidate-less row that was not `no_match`
as `Error: <error_message or Unknown>`. The unreviewable bucket's
`no_candidates` and `resolved_no_candidates` rows are not errors and the server
sends no `error_message` for them (it sets one only for `decode_error`), so all
of them read "Error: Unknown". Every view now labels them by status
("No candidates cached", "Reviewed — no candidate left"), and a decode error
shows the server's reason.
