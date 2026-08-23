### Bulk book-merge shows "Merged all" even when individual merges failed

`web/src/components/dedup/DedupBookTab.tsx` — `handleMergeSelected` (~:107) and
`handleMergeAll` (~:129) both loop over groups, catch per-group failures into
`setError(...)`, then unconditionally call `setMergeSuccess('Merged ...')` after
the loop. A run where 4 of 10 groups failed shows a success banner and a stale
error banner side by side, with no indication which groups actually merged.
`fetchDuplicates()` then re-lists, so the failed groups silently reappear
underneath the success message.

- [ ] Track per-group outcomes in the loop and report "Merged N of M" (naming the
      failures) instead of an unconditional success string.

**Why this is worth doing now rather than later:** until #2736, `api.mergeBooks`
returned the response envelope instead of `body.data`, so `initial.id` was
`undefined` on *every* invocation and the catch fired every time. The
success-after-error path was therefore permanently active and obvious to anyone
using the tab. #2736 fixed the id, which turns an always-on bug into a rare
latent one — less visible, not less wrong. Found by a silent-failure review of
#2736; pre-existing, deliberately left out of that PR's scope.
