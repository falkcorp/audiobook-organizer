<!-- file: changelog.d/20260821_083000_manual_metadata_search.md -->
<!-- version: 1.0.0 -->
<!-- guid: be17b920-b2e6-41f9-b7ab-02e660e2d15e -->
<!-- last-edited: 2026-08-21 -->

### Added

#### Search for a book's metadata yourself, from the review workspace

Automatic fetching keys off a book's **own tags**, which means it cannot rescue
a book whose tags are the problem. This library has plenty of those: author
fields holding a release-group tag (`[PZG]`, 274 books), a studio name
(`Big Finish Productions`, 426), or the book's own title
(`The Way of Shadows`, 310). Those rows sit at `no_match` permanently,
because every automatic retry asks the same wrong question and gets the same
answer.

The only way to type a corrected query was a dialog on the Library page. Phase 7
kept that dialog for exactly this reason — it and its bulk sibling are the only
callers of `searchMetadataForBook` in the frontend — but the workspace still
had no way to reach it, so the review cache could drain and never refill from
the screen built to review it.

Unmatched rows now carry a search action that opens the existing dialog against
that book. Matched rows deliberately do not: they already have a candidate
awaiting a decision, and offering a manual search there invites re-litigating a
call the reviewer has not made yet.

Worth knowing when you go looking for it: those rows are hidden by **two**
defaults, not one. `hideNoMatch` is on, and a `no_match` row is additionally
seeded with the row state `rejected`, which `hideRejected` also filters. Clear
both to work the unmatched pile.
