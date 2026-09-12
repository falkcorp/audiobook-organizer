### Added

#### Maintenance op: purge narrators that no book links to

New `maintenance.purge-empty-narrators` op deletes narrator rows attached to
zero books. It is a dry run by default (`apply=true` deletes; `limit` caps a
run). A narrator is a candidate only when an unfiltered count of the
`book_narrators` junction finds no link to it in any book state: trashed books,
non-primary versions and junction rows whose book is gone all count. The op
refuses to run if that count cannot be computed.

By default it also holds back a zero-link narrator whose name still appears in
some book's narrator text (`require_no_name_match=false` includes them). On
apply it holds the scan stand-down and re-checks each narrator's links
immediately before deleting it, so a narrator linked while the op runs is kept
and reported as "linked during run". Before each delete it writes an undo-ledger
row (`narrator_delete`, `"<id>:<name>"`) to `operation_changes`, the same shape
`purge-empty-authors` writes; if that write fails the narrator is kept. An apply
with nothing eligible returns without taking the scan stand-down.
