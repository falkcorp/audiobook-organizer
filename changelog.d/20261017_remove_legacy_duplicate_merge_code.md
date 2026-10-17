### Removed

#### Unused legacy duplicate-merge code in `internal/dedup` and the dedup maintenance job

Removed the unused legacy duplicate-merge code: `dedup.MergeBooks` and its helpers (`guardKeeperAudioRoute`, `retireMergedLoser`, `bookAudioPaths`, the `TransferITunesMetadataFirstWin` forwarder and `BookMergeResult`), the unused duplicate-author scorers (`areAuthorsDuplicate`, `jaroWinklerBelowThreshold`, `BuildAuthorSeriesMap`, `FindDuplicateAuthorsWithSeries`), the unused `CollectEmbedding` and `CollectDuration` collectors, four unused engine methods (`hasKnownShortDuration`, `isPartVsWholeMismatch`, `acoustIDSignaturesConflict`, `bookSignature`) and the `ddMergeDuplicateBook` wrapper in the dedup maintenance job. The live merge path (`internal/merge`) is unchanged and now has tests for its two safety refusals: a file-less keeper while a loser has audio, and a loser sharing audio paths with the keeper.
