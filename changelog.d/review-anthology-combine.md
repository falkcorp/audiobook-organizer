<!-- file: changelog.d/review-anthology-combine.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b7e0c39-8a15-4d62-9f30-2e6c1b8d5a47 -->
<!-- last-edited: 2026-07-26 -->

### Changed

#### Review queue: anthologies now combine into one book; clearer disc-vs-chapter labels

**Anthology = one book.** An anthology / collection / omnibus is a single real
audiobook (one ISBN), not multiple works to split apart (owner decision). Approving
an anthology review hold now **combines** its files into one multi-file book — the
same safe, tested operation as a multi-disc collapse — instead of doing nothing. The
proposed-action text changed from "split into separate works" to "combine into one
multi-file audiobook (anthology/collection)", `regroup.anthology` is wired to the
combine handler, and anthology payloads now carry sequential chapter order (disc 0,
tracks 1..N). The over-merge guard is unchanged: a plain author folder of distinct
books with no anthology marker still never combines.

**Clearer labels.** The review summary now distinguishes a genuine multi-disc set
("Multi-disc: N discs → 1 book") from same-disc sequential chapters ("Chapters: N
tracks → 1 book"), which previously both read "Multi-disc" and caused confusion.

**Known edge (flagged for follow-up):** the anthology marker regex also matches
"trilogy" / "boxed set" / "quartet", which can be *multiple* books (multiple ISBNs),
not one. Those would now be *suggested* for combine — but every anthology hold is a
human-reviewed decision (nothing auto-applies; the global `review_apply_enabled`
switch is off), so a real trilogy is caught on review and rejected. A future refinement
could split single-book markers (anthology/collection/omnibus → combine) from
multi-book markers (trilogy/boxed set → offer keep-separate), folding into the planned
ambiguous-resolution choices.
