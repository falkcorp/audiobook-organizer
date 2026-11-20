### Fixed

- The native acoustid window backfill kind is capped by the kernel at 24 hours per attempt, but the existing operation was allowed 72. It now stops starting new files a margin before each attempt's deadline, waits for the files already leased to remote workers, saves where it got to and continues in a fresh attempt, so a full-library run of up to 72 hours in total finishes instead of being cut at 24. A run that has used its 72 hours ends with an error that says so. The kind is not registered yet, so nothing changes until it is.
