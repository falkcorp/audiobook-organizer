<!-- file: changelog.d/20260820_201000_review_refetch_stale.md -->
<!-- version: 1.0.0 -->
<!-- guid: c50b8e17-2a94-4d63-b8f1-7e309a4c6d25 -->
<!-- last-edited: 2026-08-20 -->

### Added

#### Refetch stale metadata without leaving `/review`

The stale chip's tooltip has always ended "refetch to be sure", which named a
remedy the workspace had no way to reach: the only metadata fetch entry points
were two dialogs on the Library page. On production that chip reads 5,771 of
5,774 reviewable rows, so the sentence was pointing at almost the entire queue
and offering nothing.

The chip is now the path. Clicking it asks for confirmation — naming the count,
because "refetch stale" reads as a tidy-up until you see that it is thousands of
calls to external metadata providers — and then starts the existing batch fetch
as a background operation. Individual stale rows carry their own refetch button
and go straight through; one book is not worth a dialog.

No server change was needed. The lane already loads the whole review set
(`limit=0`, paginated client-side), so the full stale set is in hand without a
round trip, and `batchFetchCandidates` already accepts explicit book IDs.

Which rows count as stale is the part worth stating: the predicate is
`is_fresh === false`, not falsy. The payload distinguishes three states — stale,
fresh, and *no age at all* — and sweeping a row with no age into a bulk provider
fetch would act on a claim the server never made. The chip's own visibility
still follows the server's `stale` count, so when the two disagree the count is
still reported but no action is offered.
