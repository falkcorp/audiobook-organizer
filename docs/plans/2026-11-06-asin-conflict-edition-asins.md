<!-- file: docs/plans/2026-11-06-asin-conflict-edition-asins.md -->
<!-- version: 1.0.0 -->
<!-- guid: be08c18c-b4f4-46e6-a141-1de4a16de510 -->
<!-- last-edited: 2026-11-06 -->

# Plan: ASIN conflict → edition ASIN (owner decision 2026-11-06)

**Status: PARKED** until after the audiocore split (owner, 2026-11-06). Open question for the owner: store edition ASINs on the book only (this plan), or on versions too.

## Goal

A bulk, review, or transcription metadata apply no longer refuses with
`asin_conflict` when the book already has ASIN A and the matched Audible record
has ASIN B. Instead, the book keeps A, records B as one of its **edition
ASINs**, and the apply goes ahead on the other gate legs. If another book in the
library already has B, the pair is queued in the Dupes lane as "merge as
versions". It is never merged silently.

Owner wording: "the gate should then just merge as versions automatically so
that we don't ever hit that error". Chosen option: "Edition: keep A, store B".

## Facts (from the code at main, 2026-11-06)

- `internal/metafetch/service_apply.go:188-190` overwrites `book.ASIN`
  unconditionally. Fill-only does not protect it (`fill_only.go:42`), so today
  the gate's `asin_conflict` is the only thing stopping an overwrite.
- `applygate.CheckASIN` (`evidence.go:436`) blocks whenever A != B. An owner
  review cannot lift it (`owner_review.go:93`).
- Callers:
  - bulk gate (`evidence.go:120`)
  - `metabatch/usable_candidate.go:77`
  - `version_twin_metadata_fixer.go:775`
  - transcription auto-apply (`server_maintenance_deps.go:911`)
  - single-book dialog (`handlers/metadata/candidate_checks.go:84`, with the
    explicit `override_asin_conflict` confirm)
- The `Book` has a single `ASIN *string` (`store.go:223`). Nothing models an
  alternate or edition ASIN.
- `book:asin:<asin>:<bookID>` is a set index (`pebble_store_isbn_index.go`), read
  through `GetBookIDsByISBNASIN`.
- Dupes lane queueing: `EmbeddingStore.EnqueueManualCandidate(entityType, aID,
  bID, note)` (`embedding_store_manual.go:187`) is idempotent and pinned.

## Design

1. **Data.** Add `Book.EditionASINs []string` (`json:"edition_asins,omitempty"`).
   Index each entry in `book:asin:` too, so an ASIN lookup finds a book by any
   of its editions. The write, delete and update index paths cover the list.
   - *Departure from the option text:* I am **not** adding `BookVersion.ASIN`.
     B comes from a metadata match, not from files, so nothing says which
     version's files B describes. Stamping it on a version would invent a fact.
     If you want it anyway, the version ASIN could later be set from a version's
     own tags (the files' embedded ASIN). That would be a separate step.
2. **Gate.** `CheckASIN` becomes:
   - **agree** when B is A or is in `EditionASINs`;
   - **edition** (a new outcome: does not block, does not count as an agreement)
     when A and B differ;
   - **unknown** when either side is empty, as now.

   The `ReasonASINConflict` constant and its refusal record are retired: cut
   over, no compatibility shim. Every other leg is unchanged: title, author,
   runtime, partial_book, owner_rejected and identity_stale.
   - An **unreviewed** apply (auto or bulk) still has to be `Allowed` on those
     other legs. A wrong match (a candidate with a different title and author) is still refused
     there by title and author evidence, not by the ASIN.
   - An **owner-reviewed** row works as owner review does today: it lifts the
     certainty legs. The preview and the review row show "new edition ASIN B
     (keeps A)", so the owner sees the ASIN before clicking.
3. **Apply.** In `service_apply.go`, when the book already has a different
   non-empty ASIN, keep it, append B to `EditionASINs` (deduplicated, case
   folded), and record the change in history as an `edition_asins` field change
   (undo works). Locked ASIN: as now, the candidate's ASIN is stripped, so
   nothing is added.
   - The single-book dialog's explicit `override_asin_conflict` keeps its
     meaning, "switch the book to B". It now also pushes the old A onto
     `EditionASINs` instead of losing it.
4. **Dupes.** After an apply adds B, run `GetBookIDsByISBNASIN("", "", B)`. For
   each other live book found, call
   `EnqueueManualCandidate("book", id, other, "shares Audible edition ASIN B")`.
   This is a candidate for the owner to merge as versions; nothing auto-merges.
5. **Other callers:**
   - `metabatch/usable_candidate.go`: treats an edition as usable.
   - `version_twin_metadata_fixer.go`: the same rule. Its copy across twins must
     not overwrite a twin's ASIN; it adds an edition instead.
   - transcription auto-apply: the same as bulk (keep A, add B).
6. **UI.**
   - Preview and change list: show the `edition_asins` change.
   - Book detail: list the edition ASINs (each one links to Audible).
   - The review row: the "ASIN conflict" chip becomes "new edition ASIN".

## Files

- `internal/database/store.go` (Book field)
- `internal/database/pebble_store_isbn_index.go` (index the list)
- `internal/applygate/evidence.go`, `owner_review.go`, `refusal.go`
- `internal/metafetch/service_apply.go`, `apply_preview.go`, `apply_fields.go`,
  `field_locks.go`
- `internal/metabatch/usable_candidate.go`
- `internal/plugins/maintenance/version_twin_metadata_fixer.go`
- `internal/server/server_maintenance_deps.go`
- `internal/server/handlers/metadata/candidate_checks.go`, `handler.go`,
  `book_scan_lock.go`
- `internal/server/batch_apply_one.go` (the dupes enqueue after the apply)
- web: the review row chip, the apply preview, book detail
- tests, a changelog fragment, `docs/` (the gate reference)

## Order

1. The data field and index, with store tests.
2. The gate change, with the applygate tests rewritten for agree, edition and
   unknown.
3. The apply path (keep A, add B) and history/undo, with metafetch tests.
4. The other callers: transcription, metabatch, the twin fixer, the single-book
   override.
5. The dupes enqueue after the apply.
6. The UI.
7. Docs and the changelog.

## Test strategy

Unit tests per step, using Batman fixtures only: for example a book "The Long
Halloween" with ASIN B000BATMAN and a candidate with B000BATMN2. The cases:

- the bulk apply keeps A, adds B, and fills the empty fields;
- a second book that has B gets a Dupes candidate;
- a wrong-title candidate is still refused;
- a locked ASIN adds nothing;
- undo removes B;
- the single-book override switches to B and keeps A as an edition.

Full suites run on Woodpecker.

## Rollback

Revert the PR. `edition_asins` is an additive JSON field that old code ignores.
The extra `book:asin:` index rows only make `GetBookIDsByISBNASIN` find more
books. Dupes candidates it queued stay as normal manual candidates, which the
owner can dismiss.

## Verification in prod

After the deploy, re-run the apply on the review-page rows that hit
`asin_conflict` on 2026-11-06 (the owner has the list; it is kept out of this
public repo). Expect "applied" for the two same-title rows: each book keeps its
ASIN, `edition_asins` contains the new one, and the op log no longer says
"refused by certainty gate" for them. The third row, a different-title match,
must still be refused on an unreviewed apply.
