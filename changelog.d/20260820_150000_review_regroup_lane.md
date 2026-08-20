<!-- file: changelog.d/20260820_150000_review_regroup_lane.md -->
<!-- version: 1.0.0 -->
<!-- guid: d94dc01c-a285-47d1-be52-09c7a583bb51 -->
<!-- last-edited: 2026-08-20 -->

### Added

#### The review queue is now the third lane of `/review`

Regroup holds render in the unified workspace alongside metadata and dupes, with the
same recommendation panel, evidence, per-hold action override and bulk actions the
standalone page had. Every lane now lives in one screen; nothing points at an older
surface any more.

### Fixed

#### "Approve all" said 484 and meant 714

Bulk approve is scoped by hold type on the server, so it acts on every pending hold of
that type — but the queue only ever loaded the first 500 holds and labelled each bucket
with what it had loaded. On the current library that is a bucket reading 484 over a
button that decides 714, with no way to tell from the screen.

The bucket now shows both numbers when they differ, and the button names the scope it
actually has: **"Approve all 714"**. The per-type totals come from the count the sidebar
badge already polls, so the honest number costs nothing to obtain.

#### One bucket's skipped-holds report erased another's

Bulk approve runs each hold's own recommendation and refuses the undecidable ones, then
lists what it skipped so those holds can be handled by hand. That list was stored in a
single slot, so acting on a second bucket silently discarded the first bucket's list
while those holds were still sitting there waiting for a decision. Reports are now kept
per type and dismissed individually.

#### Leaving the lane no longer leaves its request running

The review queue's fetch could not be cancelled — the request simply ran to completion
against a lane nobody was looking at. It now aborts, like the other two lanes.
