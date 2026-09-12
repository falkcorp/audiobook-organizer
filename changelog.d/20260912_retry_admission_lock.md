### Fixed

#### Retry and a scheduled start of the same operation can no longer queue two runs

Retry on an interrupted operation first checks that no other run of the same
operation is queued or running, then re-queues the row. A scheduled or
automatic start of the same operation does the same check before it inserts a
new row. The two checks did not exclude each other. A scheduled start that
landed between Retry's check and its re-queue saw nothing active and inserted a
second run, and the two then ran back to back. Both paths now hold a per-operation
admission lock from the check to the write, so whichever goes second sees the
other's row and reuses it.

The guard that refuses Retry while a watchdog-abandoned run of the same
operation is still executing now has a test. Removing that guard would let a
second run of the same operation id start next to the first.
