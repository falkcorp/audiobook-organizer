### Security

#### Bulk metadata and review applies now refuse anything over a configurable cap

Fourteen paths that apply a whole *list* of changes at once — the cached-candidate
batch apply (op and HTTP handler), the reconcile apply, the batch-apply-candidates
handler, the batch metadata update and bulk metadata fetch endpoints, the audiobook
batch-update and batch-operations endpoints (the web UI's bulk edit), the dedup
"merge filtered" bulk merge, the diagnostics apply-suggestions endpoint, the metadata
upgrade sweep, the auto-match-transcribed maintenance op, and the review queue's bulk
approve/reject and replay-approved — now check the size of its target list *before* the first write and refuses the whole
request if it exceeds `bulk_apply_max_items` (default **5,000**). A refusal is a real
error (HTTP `422 BULK_APPLY_CAP_EXCEEDED`, or an `applycap.ExceededError` from an op),
not a truncation: zero items are written. `0` or a negative value means the default,
never unlimited. The queued-params merge for `batch-apply-cached` declines to union two
runs whose combined list would exceed the cap, so the second run queues on its own
instead of being silently folded into an oversized one. The diagnostics gate counts
*book writes*, not approved suggestion ids — a `merge_versions` suggestion writes every
book in its group, so five approved suggestions can be thousands of rows. Dry runs are not capped and the
replay dry run now reports `apply_cap` so an operator can size a `limit` under it.

The cap is a fail-safe, not a fix for any single bug: several past incidents were an
optional filter turning inert and a whole-library list being applied. With the cap, that
class of bug stops at 5,000 rows instead of the whole library. Excluded from this PR on
purpose (each is its own gate and its own test): the bulk tag write-back to files, the
metadata import endpoint, the AI author-merge, and `ApplyScanResults`.
