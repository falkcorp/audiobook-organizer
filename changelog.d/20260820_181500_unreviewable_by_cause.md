<!-- file: changelog.d/20260820_181500_unreviewable_by_cause.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f7b41c8-9e05-4d63-8a17-c4b0e396d25f -->
<!-- last-edited: 2026-08-20 -->

### Changed

#### The review rail now says why entries are unreviewable, not just how many

`unreviewable` was computed by subtracting the reviewable rows from the total.
The number was right and useless: on the live library it read 8,532 with nothing
to say about what caused it. The three cases need opposite remedies — a row
whose book is gone can only be reaped, a row with no stored candidate can be
refetched — and the subtraction happened far from the code that knew which was
which.

Each cause is now counted where it happens and reported as
`unreviewable_by_cause`. The chip's tooltip names the actual split (3,354
orphaned, 5,178 with no candidate) and the remedy each one calls for.
`unreviewable` itself is unchanged: it is now summed from the causes rather than
inferred, which is the same value.
