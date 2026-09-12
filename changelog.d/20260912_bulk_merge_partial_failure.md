### Fixed

#### Bulk book-merge reports "Merged N of M; K failed" instead of an unconditional success

On the Duplicates → Books tab, "Merge Selected" and "Merge All" always finished with a success banner ("Merged all duplicate books", "Merged N selected group(s)"), even when individual groups failed. The UI was losing failures in three ways:

1. The success string was set after the loop with no check.
2. `pollOperation` resolves, rather than throwing, when a merge op ends `failed`, `canceled` or `interrupted_*`. The loop never looked at the returned status, so a merge op that failed on the server was counted as a success and raised no error.
3. The per-group errors that were caught went into the page's `error` state. `fetchDuplicates()`, which runs right after the loop, clears that state first, so the errors were wiped. The failed groups then reappeared in the list under a success message.

The server already reported the outcome of each group, because each group is its own `dedup.book-merge` operation, so there is no server change. The tab now records each group's outcome. It shows a success banner only when every attempted group completed. If any group failed, it shows a warning (or an error when none succeeded) reading "Merged N of M group(s); K failed:" and lists each failed group's title with the reason. The count is based on the groups actually attempted, not on how many were selected. The single-group "Merge" button now also treats `canceled` and `interrupted_*` as failures instead of showing "Merged duplicates of …", and its failure (an op that did not complete, or a rejected request) goes into the same report rather than the page `error`, so pressing Refresh no longer erases it.
