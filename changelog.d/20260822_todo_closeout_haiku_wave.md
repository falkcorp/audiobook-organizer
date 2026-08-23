### Changed

#### Close out the six TODO items retired by the Haiku wave

Five source items are marked done and one is annotated in place: the hardcoded ABS
`timeBase`, the `CollectDuration` tagStore widening, the dead `store.go:17` doc
reference, the unused `internal/scanner/mocks` package, ABS gap N-5, and the
`ChapterConsolidationThresholdMin` factory-reset omission.

N-5 is struck through rather than checkbox-ticked because it is a numbered
sub-item under the ABS coverage-gaps group, matching how N-6 was already closed.
The `ChapterConsolidationThresholdMin` finding is annotated inline rather than
ticked because it is one of seven findings inside the CFG-AUDIT triage group —
ticking the group would have falsely closed the other six.

The wave merged seven PRs but retires six TODO items. TASK-015 (stop committing
`series_dedup.py`'s generated caches) shipped in it and closes no checkbox: its
source line is the REPO-SIZE-1 numbered entry, which is a stop-for-human
decision the task does not resolve.
