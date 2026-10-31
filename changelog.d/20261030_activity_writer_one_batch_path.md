### Fixed

- The activity log writer no longer carries an unreachable batching branch in its log-line path; batching goes only through `LogBatch`. No behaviour change.
