### Metadata review: a book can be dispatched to apply twice

`applyOne` pushes a book into a 500ms debounce queue; `applyMany` dispatches
immediately and does not drain that queue. Neither clears the other's pending
work, and `applyOne` does not clear `selectedIds`.

Repro: tick row B1's checkbox, click B1's row-level Apply, then within 500ms
click "Apply Selected" over a selection that still contains B1. `applyMany`
dispatches `batchApplyFromCache` with B1 now; the still-armed debounce timer
fires 500ms later and dispatches B1 again.

Client row state stays correct (the in-flight refcount is balanced -- 2 retains,
2 releases), so this does not reproduce the hidden-forever bug. The open
question is the server: is `batch-apply-cached` idempotent for a book already
applied by an in-flight op, or does the second request duplicate work / race
the first's write-back?

Found by review on PR #2954. Pre-existing, not introduced there.

- [ ] Confirm server-side idempotency for a repeated apply of the same book
- [ ] If not idempotent, have `applyMany` drain `applyQueueRef` (and cancel the
      timer) for ids it is about to dispatch
