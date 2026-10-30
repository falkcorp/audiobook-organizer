### Fixed

- The per-file intro transcription op no longer writes its run statistics out of order. Two workers finishing a batch at the same moment could save the older counters last, so the stats page briefly showed numbers going backwards mid-run. Saving a snapshot is now one ordered step.
