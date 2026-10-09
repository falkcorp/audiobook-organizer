<!-- file: docs/proposals/2026-10-holistic/01-legacy-and-dead-code/B-go-unreachable-functions.md -->
<!-- version: 1.1.1 -->
<!-- guid: e9a08669-520b-4b1c-b381-a1f0e354c62c -->
<!-- last-edited: 2026-10-09 -->

# Appendix B: Go functions unreachable from any main

Source: `deadcode ./...` (x/tools v0.51.0) at f7211eb39. Excludes `pkg/`, `internal/writeback/`, test-support files (`mock_store.go`, `testutil`, `*test` packages, `conformance`, `rapidgen`), and `*ForTest*` names. Lines = go/ast span incl. doc comment. `T` = also unreachable from tests (`deadcode -test`).

| category | funcs | lines |
|---|---|---|
| A superseded | 81 | 1950 |
| B unwired (owner) | 111 | 2161 |
| C dedup (see 03) | 15 | 705 |
| D per-item check | 279 | 3162 |
| S counted in SQLite backend (tier 3) | 1 | 16 |
| D test seam (keep) | 12 | 104 |

| lines | location | function | category | T |
|---|---|---|---|---|
| 199 | `internal/database/nuts_activity_store.go:468` | `NutsActivityStore.CompactByDay` | A superseded |  |
| 146 | `internal/database/pebble_activity_backfill.go:55` | `BackfillNutsActivityToPebble` | A superseded |  |
| 144 | `internal/database/nuts_activity_store.go:699` | `NutsActivityStore.RecompactDigests` | A superseded |  |
| 119 | `internal/database/nuts_activity_store.go:251` | `NutsActivityStore.Summarize` | A superseded |  |
| 59 | `internal/database/nuts_activity_store.go:182` | `NutsActivityStore.Query` | A superseded |  |
| 57 | `internal/database/nuts_activity_store.go:119` | `NutsActivityStore.Record` | A superseded |  |
| 52 | `internal/database/nuts_activity_store.go:890` | `NutsActivityStore.queryByIndex` | A superseded |  |
| 50 | `internal/database/nuts_activity_store.go:943` | `NutsActivityStore.findExistingDigest` | A superseded |  |
| 47 | `internal/database/nuts_metrics_store.go:103` | `NutsMetricsStore.GetCacheStatsHistory` | A superseded | T |
| 39 | `internal/database/nuts_activity_store.go:434` | `NutsActivityStore.WipeAllActivity` | A superseded |  |
| 38 | `internal/database/nuts_activity_store.go:361` | `NutsActivityStore.Prune` | A superseded |  |
| 34 | `internal/server/validators.go:136` | `ValidateSliceLength` | A superseded |  |
| 33 | `internal/database/nuts_activity_store.go:856` | `NutsActivityStore.scanTierKeysAndValues` | A superseded |  |
| 30 | `internal/database/nuts_metrics_store.go:73` | `NutsMetricsStore.RecordCacheStatsSnapshots` | A superseded | T |
| 29 | `internal/server/validators.go:184` | `ValidateURL` | A superseded |  |
| 28 | `internal/database/nuts_activity_store.go:399` | `NutsActivityStore.GetDistinctSources` | A superseded |  |
| 26 | `internal/database/activity_store_instrumented.go:57` | `InstrumentedActivityStorer.Query` | A superseded |  |
| 26 | `internal/database/nuts_activity_store.go:41` | `isNutsEmptyScan` | A superseded |  |
| 26 | `internal/server/validators.go:27` | `ValidateTitle` | A superseded |  |
| 24 | `internal/database/activity_store_instrumented.go:170` | `InstrumentedActivityStorer.WipeAllActivity` | A superseded |  |
| 22 | `internal/database/activity_store_instrumented.go:79` | `InstrumentedActivityStorer.QueryWithPartial` | A superseded |  |
| 22 | `internal/server/validators.go:94` | `ValidateEmail` | A superseded |  |
| 21 | `internal/database/dual_write_activity_store.go:168` | `DualWriteActivityStore.OptimizeStatistics` | A superseded | T |
| 21 | `internal/database/nuts_metrics_store.go:155` | `NutsMetricsStore.cacheNames` | A superseded | T |
| 21 | `internal/server/validators.go:281` | `ValidateLanguage` | A superseded |  |
| 20 | `internal/database/activity_store_instrumented.go:148` | `InstrumentedActivityStorer.GetDistinctSources` | A superseded |  |
| 20 | `internal/database/dual_write_activity_store.go:66` | `DualWriteActivityStore.Record` | A superseded |  |
| 20 | `internal/database/dual_write_activity_store.go:127` | `DualWriteActivityStore.WipeAllActivity` | A superseded | T |
| 20 | `internal/server/validators.go:228` | `ValidateDuration` | A superseded |  |
| 19 | `internal/database/activity_store_instrumented.go:31` | `InstrumentedActivityStorer.Record` | A superseded |  |
| 19 | `internal/database/activity_store_instrumented.go:191` | `InstrumentedActivityStorer.CompactByDay` | A superseded |  |
| 19 | `internal/database/activity_store_instrumented.go:212` | `InstrumentedActivityStorer.OptimizeStatistics` | A superseded |  |
| 19 | `internal/database/dual_write_activity_store.go:106` | `DualWriteActivityStore.Prune` | A superseded | T |
| 19 | `internal/database/nuts_activity_store.go:85` | `NewNutsActivityStore` | A superseded |  |
| 19 | `internal/server/validators.go:54` | `ValidatePath` | A superseded |  |
| 19 | `internal/server/validators.go:74` | `ValidateID` | A superseded |  |
| 19 | `internal/server/validators.go:261` | `ValidateGenre` | A superseded |  |
| 18 | `internal/database/activity_store_instrumented.go:107` | `InstrumentedActivityStorer.Summarize` | A superseded |  |
| 18 | `internal/database/activity_store_instrumented.go:126` | `InstrumentedActivityStorer.Prune` | A superseded |  |
| 18 | `internal/database/activity_store_instrumented.go:262` | `InstrumentedActivityStorer.RepairActivityIndexes` | A superseded |  |
| 18 | `internal/server/validators.go:117` | `ValidateInteger` | A superseded |  |
| 17 | `internal/database/dual_write_activity_store.go:86` | `DualWriteActivityStore.Summarize` | A superseded | T |
| 17 | `internal/database/dual_write_activity_store.go:145` | `DualWriteActivityStore.CompactByDay` | A superseded | T |
| 17 | `internal/database/dual_write_activity_store.go:192` | `DualWriteActivityStore.RecompactDigests` | A superseded | T |
| 17 | `internal/database/dual_write_activity_store.go:210` | `DualWriteActivityStore.RepairActivityIndexes` | A superseded | T |
| 16 | `internal/database/activity_store_instrumented.go:245` | `InstrumentedActivityStorer.RecompactDigests` | A superseded |  |
| 16 | `internal/database/nuts_metrics_store.go:36` | `NewNutsMetricsStore` | A superseded | T |
| 14 | `internal/database/nuts_metrics_store.go:60` | `ensureBucket` | A superseded |  |
| 13 | `internal/database/activity_store_instrumented.go:230` | `InstrumentedActivityStorer.MigrateSystemActivityLogs` | A superseded |  |
| 13 | `internal/database/activity_store_instrumented.go:281` | `InstrumentedActivityStorer.Close` | A superseded |  |
| 13 | `internal/database/nuts_activity_store.go:116` | `NutsActivityStore.Close` | A superseded |  |
| 13 | `internal/database/nuts_activity_store.go:685` | `NutsActivityStore.RepairActivityIndexes` | A superseded |  |
| 13 | `internal/database/nuts_activity_store.go:843` | `NutsActivityStore.scanTier` | A superseded |  |
| 13 | `internal/server/validators.go:214` | `ValidateYearRange` | A superseded |  |
| 12 | `internal/server/validators.go:171` | `ValidateStringInList` | A superseded |  |
| 11 | `internal/server/validators.go:249` | `ValidateRating` | A superseded |  |
| 10 | `internal/database/dual_write_activity_store.go:54` | `NewDualWriteActivityStore` | A superseded |  |
| 10 | `internal/database/pebble_activity_backfill.go:196` | `IsActivityPebbleBackfillDone` | A superseded |  |
| 9 | `internal/database/dual_write_activity_store.go:246` | `DualWriteActivityStore.Close` | A superseded | T |
| 9 | `internal/database/pebble_activity_backfill.go:208` | `lastIndexByte` | A superseded |  |
| 7 | `internal/database/dual_write_activity_store.go:230` | `DualWriteActivityStore.Query` | A superseded |  |
| 7 | `internal/database/dual_write_activity_store.go:238` | `DualWriteActivityStore.GetDistinctSources` | A superseded | T |
| 6 | `internal/database/dual_write_activity_store.go:184` | `DualWriteActivityStore.MigrateSystemActivityLogs` | A superseded | T |
| 6 | `internal/database/nuts_activity_store.go:667` | `NutsActivityStore.OptimizeStatistics` | A superseded |  |
| 6 | `internal/database/nuts_activity_store.go:1044` | `boolInt` | A superseded |  |
| 6 | `internal/database/pebble_activity_backfill.go:219` | `ulid32` | A superseded |  |
| 4 | `internal/database/activity_store_instrumented.go:26` | `NewInstrumentedActivityStorer` | A superseded |  |
| 4 | `internal/database/nuts_metrics_store.go:150` | `NutsMetricsStore.PruneCacheStatsHistory` | A superseded | T |
| 4 | `internal/server/deluge_importer_adapter.go:23` | `NewLibraryImporterAdapter` | A superseded | T |
| 4 | `internal/server/pipeline_checkpoint.go:26` | `setCheckpoint` | A superseded |  |
| 4 | `internal/server/pipeline_checkpoint.go:31` | `hasCheckpoint` | A superseded |  |
| 4 | `internal/server/pipeline_checkpoint.go:36` | `clearCheckpoints` | A superseded |  |
| 3 | `internal/database/nuts_activity_store.go:80` | `actTimeKey` | A superseded |  |
| 3 | `internal/database/nuts_activity_store.go:671` | `NutsActivityStore.MigrateSystemActivityLogs` | A superseded |  |
| 3 | `internal/database/nuts_metrics_store.go:31` | `metsTimeKey` | A superseded | T |
| 3 | `internal/server/validators.go:22` | `ValidationError.Error` | A superseded |  |
| 2 | `internal/database/nuts_metrics_store.go:53` | `NutsMetricsStore.Close` | A superseded | T |
| 1 | `internal/database/nuts_activity_store.go:67` | `actBucket` | A superseded |  |
| 1 | `internal/database/nuts_activity_store.go:68` | `actOpBucket` | A superseded |  |
| 1 | `internal/database/nuts_activity_store.go:69` | `actBookBucket` | A superseded |  |
| 1 | `internal/database/nuts_metrics_store.go:29` | `metsBucket` | A superseded | T |
| 150 | `internal/versions/swap.go:48` | `RunVersionSwap` | B unwired (owner) |  |
| 88 | `internal/fingerprint/window_similarity.go:86` | `WindowSetSimilarity` | B unwired (owner) |  |
| 86 | `internal/itunes/plist_parser.go:151` | `writePlist` | B unwired (owner) |  |
| 81 | `internal/itunes/import.go:361` | `ConvertTrack` | B unwired (owner) |  |
| 69 | `internal/itunes/xml_export.go:154` | `ExportBooksToITunesXML` | B unwired (owner) |  |
| 62 | `internal/itunes/mhoh_encoding_audit.go:248` | `DeriveEncodingTable` | B unwired (owner) |  |
| 52 | `internal/deluge/importer_adapter.go:59` | `LibraryImporterAdapter.ImportPath` | B unwired (owner) |  |
| 49 | `internal/versions/fs.go:99` | `movePreservingAttrs` | B unwired (owner) |  |
| 45 | `internal/itunes/itl_diff_helpers.go:103` | `DiffMsdhInventory` | B unwired (owner) |  |
| 44 | `internal/download/sabnzbd.go:131` | `SABnzbdClient.GetJob` | B unwired (owner) |  |
| 44 | `internal/fingerprint/book_signature.go:48` | `SynthesizeBookSignature` | B unwired (owner) |  |
| 42 | `internal/plugins/deluge/import.go:22` | `Plugin.importToLibrary` | B unwired (owner) | T |
| 39 | `internal/itunes/itl_safety_contract.go:100` | `ContractVerdict.Error` | B unwired (owner) |  |
| 38 | `internal/itunes/itl.go:437` | `ValidateITL` | B unwired (owner) |  |
| 36 | `internal/telemetry/metrics_handler.go:30` | `MetricsHandler` | B unwired (owner) | T |
| 35 | `internal/itunes/import.go:443` | `extractSeriesFromAlbum` | B unwired (owner) |  |
| 33 | `internal/fingerprint/window_similarity.go:215` | `bestShiftSimilarity` | B unwired (owner) |  |
| 33 | `internal/itunes/parser.go:221` | `FindLibraryFile` | B unwired (owner) |  |
| 30 | `internal/download/qbittorrent.go:134` | `QBittorrentClient.GetTorrent` | B unwired (owner) |  |
| 30 | `internal/itunes/itl_identity.go:104` | `ComputeLibraryIdentity` | B unwired (owner) |  |
| 30 | `internal/itunes/service/importer.go:2159` | `remapWindowsPath` | B unwired (owner) |  |
| 29 | `internal/download/sabnzbd.go:40` | `SABnzbdClient.apiCall` | B unwired (owner) |  |
| 29 | `internal/itunes/xml_export.go:46` | `encodeWindowsPathToURL` | B unwired (owner) |  |
| 25 | `internal/download/deluge.go:53` | `DelugeClient.call` | B unwired (owner) |  |
| 24 | `internal/download/sabnzbd.go:219` | `SABnzbdClient.ListCompleted` | B unwired (owner) |  |
| 23 | `internal/ai/telemetry.go:29` | `WithOpenAISpan` | B kept; wired by 11 PR 7 (D55) | T |
| 23 | `internal/download/deluge.go:220` | `DelugeClient.ListCompleted` | B unwired (owner) |  |
| 23 | `internal/download/qbittorrent.go:41` | `QBittorrentClient.Connect` | B unwired (owner) |  |
| 22 | `internal/download/deluge.go:80` | `DelugeClient.Connect` | B unwired (owner) |  |
| 22 | `internal/download/qbittorrent.go:78` | `QBittorrentClient.fetchTorrents` | B unwired (owner) |  |
| 22 | `internal/download/qbittorrent.go:211` | `QBittorrentClient.RemoveTorrent` | B unwired (owner) |  |
| 21 | `internal/itunes/itl_identity.go:140` | `LoadLibraryIdentity` | B unwired (owner) |  |
| 21 | `internal/itunes/itl_identity.go:160` | `SaveLibraryIdentity` | B unwired (owner) |  |
| 21 | `internal/transcribe/classify.go:161` | `IntroClassification.TitleAgreement` | B unwired (owner) |  |
| 21 | `internal/versions/fs.go:57` | `MoveToVersionsDir` | B unwired (owner) |  |
| 20 | `internal/download/qbittorrent.go:184` | `QBittorrentClient.SetDownloadPath` | B unwired (owner) |  |
| 19 | `internal/download/deluge.go:174` | `DelugeClient.GetUploadStats` | B unwired (owner) |  |
| 19 | `internal/download/sabnzbd.go:71` | `SABnzbdClient.Connect` | B unwired (owner) |  |
| 19 | `internal/versions/swap.go:221` | `ResumeVersionSwaps` | B unwired (owner) | T |
| 18 | `internal/download/sabnzbd.go:200` | `SABnzbdClient.RemoveJob` | B unwired (owner) |  |
| 17 | `internal/download/deluge.go:133` | `delugeToTorrentInfo` | B unwired (owner) |  |
| 17 | `internal/transcribe/classify.go:187` | `IntroClassification.IsLikelyMisfiled` | B unwired (owner) |  |
| 17 | `internal/versions/swap.go:196` | `filesForVersion` | B unwired (owner) |  |
| 16 | `internal/download/deluge.go:157` | `DelugeClient.GetTorrent` | B unwired (owner) |  |
| 16 | `internal/download/qbittorrent.go:101` | `mapQBState` | B unwired (owner) |  |
| 16 | `internal/download/qbittorrent.go:165` | `QBittorrentClient.GetUploadStats` | B unwired (owner) |  |
| 16 | `internal/download/sabnzbd.go:100` | `mapSABStatus` | B unwired (owner) |  |
| 16 | `internal/itunes/parser.go:204` | `EncodeLocation` | B unwired (owner) |  |
| 16 | `internal/versions/fs.go:78` | `MoveFromVersionsDir` | B unwired (owner) |  |
| 15 | `internal/deluge/importer_adapter.go:37` | `NewLibraryImporterAdapter` | B unwired (owner) |  |
| 15 | `internal/deluge/integration.go:164` | `NotifyDelugeAfterVersionSwap` | B unwired (owner) |  |
| 15 | `internal/download/deluge.go:117` | `mapDelugeState` | B unwired (owner) |  |
| 15 | `internal/itunes/import.go:479` | `computeFileHash` | B unwired (owner) |  |
| 14 | `internal/download/qbittorrent.go:118` | `qbToTorrentInfo` | B unwired (owner) |  |
| 14 | `internal/download/sabnzbd.go:176` | `SABnzbdClient.GetQueueStats` | B unwired (owner) |  |
| 14 | `internal/fingerprint/window_similarity.go:166` | `splitHead` | B unwired (owner) |  |
| 14 | `internal/fingerprint/window_similarity.go:197` | `setDuration` | B unwired (owner) |  |
| 13 | `internal/download/factory.go:14` | `NewTorrentClientFromConfig` | B unwired (owner) |  |
| 13 | `internal/itunes/xml_export.go:31` | `formatToKind` | B unwired (owner) |  |
| 13 | `internal/itunes/service/importer.go:2190` | `calculatePercent` | B unwired (owner) |  |
| 12 | `internal/download/qbittorrent.go:234` | `QBittorrentClient.ListCompleted` | B unwired (owner) |  |
| 12 | `internal/download/sabnzbd.go:117` | `sabSlotToNZBInfo` | B unwired (owner) |  |
| 12 | `internal/itunes/itl.go:148` | `hexToPID` | B unwired (owner) |  |
| 12 | `internal/transcribe/batch.go:47` | `TranscribeBatch` | B unwired (owner) | T |
| 12 | `internal/transcribe/classify.go:144` | `IntroClassification.ShouldEnqueueTranscription` | B unwired (owner) |  |
| 11 | `internal/download/deluge.go:196` | `DelugeClient.SetDownloadPath` | B unwired (owner) |  |
| 11 | `internal/download/factory.go:28` | `NewUsenetClientFromConfig` | B unwired (owner) |  |
| 11 | `internal/download/qbittorrent.go:29` | `NewQBittorrentClient` | B unwired (owner) |  |
| 11 | `internal/download/sabnzbd.go:29` | `NewSABnzbdClient` | B unwired (owner) |  |
| 10 | `internal/itunes/smart_criteria_reader.go:108` | `SmartOperator.String` | B unwired (owner) |  |
| 9 | `internal/fingerprint/window_similarity.go:245` | `median` | B unwired (owner) |  |
| 9 | `internal/versions/fs.go:44` | `EnsureVersionsDir` | B unwired (owner) |  |
| 8 | `internal/download/sabnzbd.go:191` | `SABnzbdClient.SetDownloadPath` | B unwired (owner) |  |
| 8 | `internal/fingerprint/window_similarity.go:184` | `sameProvenance` | B unwired (owner) |  |
| 8 | `internal/itunes/fingerprint.go:32` | `ErrLibraryModified.Error` | B unwired (owner) | T |
| 8 | `internal/itunes/fingerprint.go:69` | `LibraryFingerprint.Matches` | B unwired (owner) |  |
| 7 | `internal/download/deluge.go:31` | `NewDelugeClient` | B unwired (owner) |  |
| 7 | `internal/download/deluge.go:209` | `DelugeClient.UpdateStoragePath` | B unwired (owner) |  |
| 7 | `internal/download/qbittorrent.go:206` | `QBittorrentClient.UpdateStoragePath` | B unwired (owner) |  |
| 7 | `internal/plugins/itunes/adapter.go:25` | `NewLoggerWrapper` | B unwired (owner) | T |
| 7 | `internal/versions/swap.go:210` | `filePaths` | B unwired (owner) |  |
| 6 | `internal/ai/telemetry.go:44` | `RecordOpenAIMetric` | B kept; wired by 11 PR 7 (D55) | T |
| 6 | `internal/itunes/itl_identity.go:75` | `FileSHA256Hex` | B unwired (owner) | T |
| 6 | `internal/server/deluge_integration.go:44` | `NotifyDelugeAfterOrganize` | B unwired (owner) |  |
| 6 | `internal/server/deluge_integration.go:51` | `NotifyDelugeAfterVersionSwap` | B unwired (owner) | T |
| 5 | `internal/download/deluge.go:214` | `DelugeClient.RemoveTorrent` | B unwired (owner) |  |
| 5 | `internal/fingerprint/window_similarity.go:190` | `provenanceDiff` | B unwired (owner) |  |
| 5 | `internal/plugins/itunes/adapter.go:72` | `loggerWrapper.With` | B unwired (owner) | T |
| 5 | `internal/server/deluge_integration.go:39` | `NotifyDelugeMoveStorage` | B unwired (owner) | T |
| 5 | `internal/transcribe/inflight.go:48` | `inFlightDepth` | B unwired (owner) |  |
| 4 | `internal/download/deluge.go:244` | `DelugeClient.ClientType` | B unwired (owner) |  |
| 4 | `internal/download/qbittorrent.go:247` | `QBittorrentClient.ClientType` | B unwired (owner) |  |
| 4 | `internal/download/sabnzbd.go:244` | `SABnzbdClient.ClientType` | B unwired (owner) |  |
| 4 | `internal/fingerprint/window_similarity.go:162` | `frames` | B unwired (owner) |  |
| 4 | `internal/itunes/itl_identity.go:132` | `IdentitySidecarPath` | B unwired (owner) |  |
| 4 | `internal/itunes/xml_export.go:75` | `xmlEscape` | B unwired (owner) |  |
| 4 | `internal/plugins/itunes/adapter.go:32` | `loggerWrapper.Trace` | B unwired (owner) | T |
| 4 | `internal/plugins/itunes/adapter.go:53` | `loggerWrapper.UpdateProgress` | B unwired (owner) | T |
| 4 | `internal/plugins/itunes/adapter.go:62` | `loggerWrapper.ChangeCounters` | B unwired (owner) | T |
| 4 | `internal/plugins/itunes/adapter.go:67` | `loggerWrapper.IsCanceled` | B unwired (owner) | T |
| 4 | `internal/telemetry/telemetry.go:194` | `GlobalTracer` | B unwired (owner) | T |
| 4 | `internal/telemetry/telemetry.go:199` | `GlobalMeter` | B unwired (owner) | T |
| 4 | `internal/transcribe/inflight.go:53` | `inFlightPoolExists` | B unwired (owner) |  |
| 3 | `internal/plugins/itunes/adapter.go:37` | `loggerWrapper.Debug` | B unwired (owner) | T |
| 3 | `internal/plugins/itunes/adapter.go:41` | `loggerWrapper.Info` | B unwired (owner) | T |
| 3 | `internal/plugins/itunes/adapter.go:45` | `loggerWrapper.Warn` | B unwired (owner) | T |
| 3 | `internal/plugins/itunes/adapter.go:49` | `loggerWrapper.Error` | B unwired (owner) | T |
| 3 | `internal/plugins/itunes/adapter.go:58` | `loggerWrapper.RecordChange` | B unwired (owner) | T |
| 3 | `internal/transcribe/classify.go:138` | `IntroClassification.IsVerifiable` | B unwired (owner) |  |
| 2 | `internal/transcribe/classify.go:134` | `IntroClassification.IsCredits` | B unwired (owner) |  |
| 2 | `internal/transcribe/inflight.go:58` | `resetInFlightState` | B unwired (owner) |  |
| 202 | `internal/dedup/book_dedup.go:555` | `MergeBooks` | C dedup (see 03) |  |
| 91 | `internal/dedup/collectors_embedding.go:141` | `CollectEmbedding` | C dedup (see 03) |  |
| 65 | `internal/dedup/book_dedup.go:381` | `guardKeeperAudioRoute` | C dedup (see 03) |  |
| 52 | `internal/dedup/author.go:91` | `areAuthorsDuplicate` | C dedup (see 03) |  |
| 52 | `internal/dedup/author.go:526` | `BuildAuthorSeriesMap` | C dedup (see 03) | T |
| 52 | `internal/dedup/book_dedup.go:463` | `retireMergedLoser` | C dedup (see 03) |  |
| 40 | `internal/dedup/author.go:311` | `jaroWinklerBelowThreshold` | C dedup (see 03) |  |
| 31 | `internal/dedup/engine.go:2159` | `Engine.acoustIDSignaturesConflict` | C dedup (see 03) |  |
| 28 | `internal/dedup/collectors_metadata.go:146` | `CollectDuration` | C dedup (see 03) | T |
| 28 | `internal/dedup/engine.go:2178` | `Engine.bookSignature` | C dedup (see 03) |  |
| 19 | `internal/dedup/book_dedup.go:424` | `bookAudioPaths` | C dedup (see 03) |  |
| 13 | `internal/dedup/book_dedup.go:352` | `TransferITunesMetadataFirstWin` | C dedup (see 03) |  |
| 13 | `internal/dedup/engine.go:2083` | `Engine.hasKnownShortDuration` | C dedup (see 03) |  |
| 13 | `internal/dedup/engine.go:2109` | `Engine.isPartVsWholeMismatch` | C dedup (see 03) |  |
| 6 | `internal/dedup/author.go:611` | `FindDuplicateAuthorsWithSeries` | C dedup (see 03) | T |
| 104 | `internal/scanner/scanner.go:4524` | `preserveExistingFields` | D per-item check |  |
| 83 | `internal/metadata/enhanced.go:532` | `writeM4BCustomTagsWithFFmpeg` | D per-item check | T |
| 58 | `internal/errhandling/skipcounter.go:171` | `SkipCounter.LogSummary` | D per-item check |  |
| 54 | `internal/server/server_title_helpers.go:105` | `stripChapterFromTitle` | D per-item check |  |
| 52 | `internal/fileops/copy.go:344` | `CopyFileAtomic` | D per-item check |  |
| 42 | `internal/database/tag_helpers.go:118` | `EnsureSingletonAuthorTag` | D per-item check | T |
| 42 | `internal/database/tag_helpers.go:161` | `EnsureSingletonSeriesTag` | D per-item check | T |
| 41 | `internal/plugins/maintenance/retire_into.go:68` | `retireInto` | D per-item check |  |
| 38 | `internal/diagnosis/probe.go:128` | `Classify` | D per-item check |  |
| 35 | `internal/organizer/pipeline.go:522` | `ComputeTargetPathsFromSegments` | D per-item check |  |
| 34 | `internal/metadata/custom_tags.go:42` | `CustomTags.ToMap` | D per-item check |  |
| 34 | `internal/metafetch/batch.go:59` | `BuildCandidateBookInfo` | D per-item check |  |
| 31 | `internal/server/batch_apply_one.go:655` | `applyCachedCandidateForBook` | D per-item check |  |
| 30 | `internal/maintenance/jobs/dedup_books.go:948` | `ddMergeDuplicateBook` | D per-item check |  |
| 30 | `internal/security/pathvalidation/pathvalidation.go:71` | `ValidateRelativePath` | D per-item check |  |
| 29 | `internal/sysinfo/memory.go:32` | `GetMemoryStats` | D per-item check |  |
| 28 | `internal/audioutil/drm.go:38` | `DetectDRM` | D per-item check |  |
| 26 | `internal/tagger/tagger.go:23` | `updateFileTags` | D per-item check |  |
| 25 | `internal/database/memdb_search.go:61` | `SubstringSearchMatches` | D per-item check |  |
| 25 | `internal/database/migrations.go:748` | `GetMigrationHistory` | D per-item check |  |
| 24 | `internal/audiobooks/service_filtering.go:267` | `matchesFieldFiltersRT` | D per-item check |  |
| 24 | `internal/tagger/embed_cover.go:18` | `EmbedCoverArt` | D per-item check |  |
| 23 | `internal/operations/registry/live_tracker.go:62` | `ShutdownAllForStore` | D per-item check |  |
| 23 | `internal/pathutil/commondir.go:20` | `CommonDir` | D per-item check |  |
| 22 | `internal/appdirs/appdirs.go:102` | `ClearedBaseline` | D per-item check |  |
| 22 | `internal/mediainfo/mediainfo.go:316` | `GetQualityTier` | D per-item check |  |
| 21 | `internal/audioutil/timeline.go:78` | `ShiftChapters` | D per-item check |  |
| 21 | `internal/metafetch/batch.go:106` | `LoadRejectedCandidateKeys` | D per-item check |  |
| 20 | `internal/backup/backup.go:1331` | `BackupDatabase` | D per-item check |  |
| 20 | `internal/database/credits.go:102` | `JoinCreditNames` | D per-item check |  |
| 19 | `internal/errhandling/errhandling.go:65` | `SetLogger` | D per-item check |  |
| 19 | `internal/errhandling/skipcounter.go:89` | `SkipCounter.Skip` | D per-item check |  |
| 19 | `internal/pathutil/abbreviate.go:26` | `Abbreviate` | D per-item check |  |
| 19 | `internal/plugins/maintenance/combined_author_fixer.go:1087` | `combinedNextCredits` | D per-item check |  |
| 19 | `internal/server/handlers/operations/handler.go:141` | `Handler.resolveScheduler` | D per-item check | T |
| 18 | `internal/database/ai_scan_store.go:107` | `NewAIScanStore` | D per-item check |  |
| 18 | `internal/database/book_sort.go:194` | `SortableBookFields` | D per-item check |  |
| 18 | `internal/matcher/fuzzy.go:238` | `RankResults` | D per-item check |  |
| 18 | `internal/metafetch/service_scoring.go:1045` | `BestTitleMatchWithContext` | D per-item check |  |
| 18 | `internal/operations/freshness/freshness.go:78` | `PebbleFreshness.ShouldProcess` | D per-item check |  |
| 17 | `internal/audioutil/timeline.go:36` | `CumulativeOffsets` | D per-item check |  |
| 17 | `internal/database/memdb_indexers.go:69` | `nullableBoolFieldIndex.FromObject` | D per-item check | T |
| 17 | `internal/database/memdb_indexers.go:190` | `effectiveIntFieldIndex.FromObject` | D per-item check | T |
| 17 | `internal/fingerprint/fpcalc.go:143` | `File` | D per-item check | T |
| 17 | `internal/logger/logger.go:44` | `ParseLevel` | D per-item check |  |
| 17 | `internal/logger/operation.go:159` | `OperationLogger.Log` | D per-item check |  |
| 17 | `internal/serviceregistry/container.go:94` | `Groups` | D per-item check |  |
| 17 | `internal/syncapi/progress/policy.go:258` | `MergeCombine` | D per-item check |  |
| 16 | `internal/authority/lookup.go:251` | `Index.LookupPublisher` | D per-item check |  |
| 16 | `internal/database/memdb_indexers.go:87` | `nullableBoolFieldIndex.FromArgs` | D per-item check | T |
| 16 | `internal/database/settings.go:222` | `DeriveKeyFromPassword` | D per-item check |  |
| 16 | `internal/database/sql_activity_backfill.go:138` | `BackfillPebbleActivityToSQL` | S counted in SQLite backend (tier 3) |  |
| 16 | `internal/logger/operation.go:72` | `OperationLogger.log` | D per-item check |  |
| 16 | `internal/logger/operation.go:142` | `OperationLogger.With` | D per-item check |  |
| 16 | `internal/openlibrary/types.go:72` | `DescriptionText` | D per-item check |  |
| 15 | `internal/fileops/safe_operations.go:225` | `FileOperation.Rollback` | D per-item check |  |
| 15 | `internal/operations/state.go:176` | `LoadParams` | D per-item check | T |
| 15 | `internal/operations/freshness/freshness.go:102` | `PebbleFreshness.StampBatch` | D per-item check |  |
| 15 | `internal/server/server_title_helpers.go:160` | `stripSubtitle` | D per-item check |  |
| 14 | `internal/config/protected_fields.go:36` | `FieldClass.String` | D per-item check |  |
| 14 | `internal/fileops/safe_operations.go:321` | `SafeMove` | D per-item check |  |
| 14 | `internal/httputil/parse.go:92` | `HandleBindError` | D per-item check |  |
| 14 | `internal/logger/operation.go:48` | `ForOperation` | D per-item check |  |
| 14 | `internal/metafetch/batch.go:127` | `applyRuntimeInfo` | D per-item check |  |
| 14 | `internal/metafetch/service_writeback.go:330` | `CurrentTagValues` | D per-item check | T |
| 14 | `internal/operations/freshness/freshness.go:135` | `prefixSuccessor` | D per-item check |  |
| 13 | `internal/database/memdb_indexers.go:208` | `effectiveIntFieldIndex.FromArgs` | D per-item check | T |
| 13 | `internal/database/metadata_fetch_cache.go:284` | `InvalidateCachedMetadataFetch` | D per-item check |  |
| 13 | `internal/database/settings.go:236` | `DeriveKeyFromPasswordWithSalt` | D per-item check | T |
| 13 | `internal/database/settings.go:394` | `GetDecryptedSetting` | D per-item check |  |
| 13 | `internal/errhandling/skipcounter.go:129` | `SkipCounter.Skipped` | D per-item check |  |
| 13 | `internal/fileops/copy.go:171` | `CopyFileIngest` | D per-item check |  |
| 13 | `internal/httputil/types.go:85` | `NewBulkResponse` | D per-item check |  |
| 13 | `internal/metadata/book_file_hashes.go:46` | `BookFileHashOptions` | D per-item check | T |
| 13 | `internal/metadata/folder_parser.go:86` | `ExtractMetadataFromFolder` | D per-item check |  |
| 13 | `internal/metadata/providerhttp/providerhttp.go:147` | `HasOverride` | D per-item check |  |
| 13 | `internal/scanner/scanner.go:448` | `InitWorksLookupCache` | D per-item check | T |
| 13 | `internal/search/index_builder.go:291` | `ReindexBookByID` | D per-item check | T |
| 12 | `internal/authority/lookup.go:61` | `PutPersonOverride` | D per-item check |  |
| 12 | `internal/authority/lookup.go:74` | `PutPublisherOverride` | D per-item check |  |
| 12 | `internal/database/migrations.go:586` | `recordMigration` | D per-item check |  |
| 12 | `internal/franchise/detect.go:190` | `Result.First` | D per-item check | T |
| 12 | `internal/server/file_io_pool.go:311` | `InitFileIOPool` | D per-item check | T |
| 12 | `internal/server/middleware/ratelimit.go:218` | `IPRateLimiter.Stop` | D per-item check |  |
| 11 | `internal/cache/registry.go:102` | `All` | D per-item check | T |
| 11 | `internal/database/catalog_entry_store.go:608` | `CatalogStore.GetEntryByProviderID` | D per-item check |  |
| 11 | `internal/database/keyfamilies.go:351` | `familyForKey` | D per-item check |  |
| 11 | `internal/database/metadata_fetch_cache.go:238` | `GetCachedMetadataFetch` | D per-item check |  |
| 11 | `internal/database/scan_state.go:134` | `AnyProvisionalCore` | D per-item check | T |
| 11 | `internal/errhandling/skipcounter.go:109` | `SkipCounter.Processed` | D per-item check |  |
| 11 | `internal/errhandling/skipcounter.go:153` | `SkipCounter.ByReason` | D per-item check |  |
| 11 | `internal/fileops/copy.go:157` | `CopyFileExclusive` | D per-item check |  |
| 11 | `internal/fingerprint/backfill_utils.go:39` | `IsFingerprintable` | D per-item check |  |
| 11 | `internal/metadata/cover.go:39` | `DownloadCoverArt` | D per-item check |  |
| 11 | `internal/metadata/providerhttp/providerhttp.go:131` | `KnownProviders` | D per-item check |  |
| 11 | `internal/realtime/events.go:331` | `InitializeEventHub` | D per-item check |  |
| 11 | `internal/repairs/guards.go:45` | `GuardBookPaths` | D per-item check |  |
| 11 | `internal/repairs/guards.go:543` | `GuardBooks` | D per-item check |  |
| 11 | `internal/scanner/scanner.go:502` | `ClearWorksLookupCache` | D per-item check |  |
| 11 | `internal/server/handlers/abs/play.go:134` | `Handler.SessionTimeListening` | D per-item check |  |
| 10 | `internal/ai/embedding_client.go:155` | `NewEmbeddingClient` | D per-item check |  |
| 10 | `internal/applygate/evidence.go:100` | `CheckEvidence` | D per-item check |  |
| 10 | `internal/audiobooks/service_filtering.go:416` | `fieldMatchesValueRT` | D per-item check |  |
| 10 | `internal/authority/lookup.go:308` | `Index.CanonicalPublisher` | D per-item check |  |
| 10 | `internal/config/protected_fields.go:178` | `ConfigFieldClass` | D per-item check |  |
| 10 | `internal/config/protected_fields.go:187` | `parentFieldPath` | D per-item check |  |
| 10 | `internal/database/author_bookref.go:222` | `PebbleStore.getAllAuthorBookRefCountsPebble` | D per-item check |  |
| 10 | `internal/database/credits.go:84` | `cleanNames` | D per-item check |  |
| 10 | `internal/database/embedding_store.go:2444` | `encodeVectorV0` | D per-item check |  |
| 10 | `internal/database/embedding_store.go:2555` | `vectorEncodeRatio` | D per-item check |  |
| 10 | `internal/database/pebble_store.go:6064` | `deleteFingerprintLSHIndexesByID` | D per-item check | T |
| 10 | `internal/diagnosis/probe.go:341` | `looksLikeAudio` | D per-item check |  |
| 10 | `internal/metafetch/batch.go:93` | `CountByStatus` | D per-item check |  |
| 10 | `internal/metafetch/candidate_fallback.go:31` | `IsCandidateFallbackProvider` | D per-item check | T |
| 10 | `internal/repairs/owner.go:217` | `OwnerApplyFrom` | D per-item check |  |
| 10 | `internal/server/server_metadata.go:32` | `decodeRawValue` | D per-item check |  |
| 10 | `internal/tagger/tagger.go:50` | `updateM4BTags` | D per-item check |  |
| 10 | `internal/tagger/tagger.go:61` | `updateMP3Tags` | D per-item check |  |
| 10 | `internal/tagger/tagger.go:72` | `updateFLACTags` | D per-item check |  |
| 10 | `internal/undo/restorable.go:897` | `HandOffCrownedValue` | D per-item check |  |
| 9 | `internal/authority/authority.go:166` | `PersonKey` | D per-item check |  |
| 9 | `internal/authority/lookup.go:298` | `Index.LookupASIN` | D per-item check |  |
| 9 | `internal/database/book_change_log.go:151` | `MetadataCacheChangedSinceOf` | D per-item check |  |
| 9 | `internal/database/settings.go:242` | `GenerateArgon2Salt` | D per-item check | T |
| 9 | `internal/diagnosis/probe.go:167` | `ToJSON` | D per-item check |  |
| 9 | `internal/errhandling/skipcounter.go:78` | `NewSkipCounter` | D per-item check |  |
| 9 | `internal/errhandling/skipcounter.go:119` | `SkipCounter.Add` | D per-item check |  |
| 9 | `internal/errhandling/skipcounter.go:143` | `SkipCounter.ProcessedCount` | D per-item check |  |
| 9 | `internal/fileops/safe_operations.go:34` | `DefaultConfig` | D per-item check |  |
| 9 | `internal/httputil/rangeserve_gin.go:16` | `ServeFileWithRangeGin` | D per-item check |  |
| 9 | `internal/logger/operation.go:96` | `OperationLogger.UpdateProgress` | D per-item check |  |
| 9 | `internal/metadata/metadata.go:557` | `firstNonEmpty` | D per-item check |  |
| 9 | `internal/metafetch/service_scoring.go:1042` | `BestTitleMatch` | D per-item check |  |
| 9 | `internal/operations/state.go:193` | `LoadRawParams` | D per-item check | T |
| 9 | `internal/repairs/guards.go:550` | `GuardBooksFor` | D per-item check |  |
| 9 | `internal/scheduler/daily_at.go:140` | `TaskDefinition.selfScheduled` | D per-item check |  |
| 9 | `internal/security/safepath/safepath.go:55` | `MustJoin` | D per-item check |  |
| 9 | `internal/server/handlers/aibackends/aibackends.go:61` | `WithAttribution` | D per-item check |  |
| 9 | `internal/server/middleware/absauth.go:143` | `ABSIdentityResolver.ABSCredentialPresent` | D per-item check | T |
| 9 | `internal/undo/revert_plan.go:227` | `RevertPlan.HandedOff` | D per-item check | T |
| 9 | `internal/util/pointers.go:58` | `ExtractBoolField` | D per-item check |  |
| 8 | `internal/authority/authority.go:175` | `PublisherKey` | D per-item check | T |
| 8 | `internal/backup/backup.go:725` | `RestoreBackup` | D per-item check |  |
| 8 | `internal/database/metadata_field_locks.go:96` | `UserLockableFieldKeys` | D per-item check |  |
| 8 | `internal/fileops/hash.go:56` | `GetFileSize` | D per-item check |  |
| 8 | `internal/fileops/safe_operations.go:355` | `VerifyFileIntegrity` | D per-item check |  |
| 8 | `internal/httputil/parse.go:107` | `EnsureNotNil` | D per-item check |  |
| 8 | `internal/logger/operation.go:114` | `OperationLogger.ChangeCounters` | D per-item check |  |
| 8 | `internal/logger/operation.go:123` | `OperationLogger.Changes` | D per-item check |  |
| 8 | `internal/metadata/book_name.go:125` | `BookName.SearchName` | D per-item check |  |
| 8 | `internal/metadata/hardcover.go:44` | `NewHardcoverClientWithBaseURL` | D per-item check |  |
| 8 | `internal/metadata/wikipedia.go:37` | `NewWikipediaClientWithBaseURL` | D per-item check |  |
| 8 | `internal/metafetch/service_scoring.go:730` | `ScoreOneResult` | D per-item check |  |
| 8 | `internal/operations/trigger_source.go:30` | `TriggerSourceFromContext` | D per-item check | T |
| 8 | `internal/policy/policy.go:85` | `EvaluatePolicyDetailed` | D per-item check | T |
| 8 | `internal/scanner/chapter_consolidator.go:199` | `DetectChapterGroups` | D per-item check |  |
| 8 | `internal/scanner/scanner.go:4379` | `resolveAuthorID` | D per-item check |  |
| 8 | `internal/searchcache/cache.go:915` | `Cache.jobHoldsIDs` | D per-item check |  |
| 8 | `internal/server/server_metadata.go:24` | `Server.loadMetadataState` | D per-item check |  |
| 8 | `internal/server/handlers/playlists.go:91` | `NewPlaylistHandler` | D per-item check |  |
| 8 | `internal/server/handlers/abs/browse.go:624` | `Handler.EmptyPage` | D per-item check | T |
| 8 | `internal/tagger/safe_write.go:157` | `WriteImageInPlace` | D per-item check | T |
| 7 | `internal/activity/api.go:111` | `WithTags` | D per-item check | T |
| 7 | `internal/audiobooks/service_filtering.go:409` | `fieldMatchesValue` | D per-item check |  |
| 7 | `internal/audioutil/duration.go:158` | `DurationProbeAvailable` | D per-item check |  |
| 7 | `internal/authority/authority.go:93` | `KeyPrefixes` | D per-item check |  |
| 7 | `internal/backup/backup.go:989` | `DeleteBackup` | D per-item check |  |
| 7 | `internal/catalog/harvest.go:319` | `Harvester.HarvestAuthor` | D per-item check |  |
| 7 | `internal/database/fingerprint_window.go:97` | `PathWindowRef` | D per-item check |  |
| 7 | `internal/database/pebble_store_atpath_markers.go:213` | `PebbleStore.undecodableMarkerCount` | D per-item check |  |
| 7 | `internal/database/store.go:1479` | `GetGlobalStore` | D per-item check |  |
| 7 | `internal/fingerprint/tool_equivalence.go:67` | `EquivalentToolPairs` | D per-item check |  |
| 7 | `internal/fingerprint/wholefile.go:54` | `WholeFile.FrameCount` | D per-item check |  |
| 7 | `internal/logger/operation.go:106` | `OperationLogger.RecordChange` | D per-item check |  |
| 7 | `internal/merge/sync_follow.go:204` | `FollowMergeWithStore` | D per-item check |  |
| 7 | `internal/metadata/audible.go:39` | `NewAudibleClientWithBaseURL` | D per-item check |  |
| 7 | `internal/metadata/audnexus.go:40` | `NewAudnexusClientWithBaseURL` | D per-item check |  |
| 7 | `internal/metadata/googlebooks.go:39` | `NewGoogleBooksClientWithBaseURL` | D per-item check |  |
| 7 | `internal/operations/freshness/freshness.go:119` | `PebbleFreshness.ClearStamps` | D per-item check |  |
| 7 | `internal/operations/registry/reporter_db.go:631` | `dbReporter.droppedLogCount` | D per-item check |  |
| 7 | `internal/personname/personname.go:129` | `IsNameParticle` | D per-item check | T |
| 7 | `internal/plugins/maintenance/duration_backfill.go:357` | `processBookForReextract` | D per-item check |  |
| 7 | `internal/scanner/ai_parse_async.go:64` | `EnqueueAIParseWired` | D per-item check |  |
| 7 | `internal/scanner/scanner.go:294` | `SetScanCache` | D per-item check |  |
| 7 | `internal/scanner/scanner.go:2706` | `createBookFilesForBook` | D per-item check |  |
| 7 | `internal/server/file_io_pool.go:108` | `GetGlobalFileIOPool` | D per-item check | T |
| 7 | `internal/server/server_helpers.go:29` | `validateAbsolutePath` | D per-item check |  |
| 7 | `internal/server/middleware/absauth.go:156` | `ABSIdentityResolver.Config` | D per-item check | T |
| 7 | `internal/util/pointers.go:9` | `DerefStr` | D per-item check |  |
| 7 | `internal/util/pointers.go:17` | `DerefInt` | D per-item check |  |
| 7 | `internal/util/pointers.go:25` | `DerefBool` | D per-item check |  |
| 6 | `internal/audioutil/duration.go:124` | `DurationProbeStats` | D per-item check | T |
| 6 | `internal/backup/backup.go:1315` | `ScheduleBackup` | D per-item check |  |
| 6 | `internal/database/catalog_entry_store.go:71` | `CatalogKeyPrefixes` | D per-item check |  |
| 6 | `internal/database/credits.go:118` | `JoinCreditNamesABS` | D per-item check |  |
| 6 | `internal/metabatch/upgrade.go:357` | `transcriptionConfirmsCandidate` | D per-item check |  |
| 6 | `internal/metafetch/service_normalize.go:19` | `derefString` | D per-item check |  |
| 6 | `internal/metafetch/service_normalize.go:25` | `derefIntAsString` | D per-item check |  |
| 6 | `internal/metrics/pipeline_metrics.go:138` | `IncFilenameParse` | D per-item check |  |
| 6 | `internal/operations/freshness/freshness.go:126` | `encodeNow` | D per-item check |  |
| 6 | `internal/operations/registry/types.go:449` | `WithRequires` | D per-item check |  |
| 6 | `internal/realtime/events.go:317` | `GetGlobalHub` | D per-item check |  |
| 6 | `internal/scanner/scanner.go:303` | `ClearScanCache` | D per-item check |  |
| 6 | `internal/server/path_locks.go:50` | `pathLocks.setTrace` | D per-item check |  |
| 6 | `internal/server/server_helpers.go:43` | `stringVal` | D per-item check |  |
| 6 | `internal/server/server_helpers.go:50` | `intVal` | D per-item check |  |
| 6 | `internal/server/handlers/ai.go:986` | `UnwrapAIJobsStore` | D per-item check | T |
| 6 | `internal/server/middleware/ratelimit.go:236` | `IPRateLimiter.Len` | D per-item check |  |
| 5 | `internal/ai/embedding_scorer.go:44` | `NewEmbeddingScorerWithAPI` | D per-item check |  |
| 5 | `internal/aidispatch/dispatch.go:131` | `WithRequiredLabels` | D per-item check |  |
| 5 | `internal/aidispatch/dispatch.go:137` | `WithTotalCap` | D per-item check |  |
| 5 | `internal/audiobooks/service_filtering.go:261` | `matchesFieldFilters` | D per-item check |  |
| 5 | `internal/audioutil/duration.go:54` | `FFprobeAvailable` | D per-item check |  |
| 5 | `internal/audioutil/mediainfo.go:63` | `MediaInfoAvailable` | D per-item check |  |
| 5 | `internal/backup/codec.go:81` | `SupportedCompression` | D per-item check |  |
| 5 | `internal/database/catalog_entry_store.go:710` | `CatalogStore.CountEntries` | D per-item check |  |
| 5 | `internal/database/chromem_embedding_store.go:220` | `MetaInt` | D per-item check | T |
| 5 | `internal/database/pebble_file_provenance.go:480` | `SortFileEvents` | D per-item check | T |
| 5 | `internal/database/storage_format.go:132` | `StorageMigrationCheckpointPath` | D per-item check | T |
| 5 | `internal/fingerprint/backfill_utils.go:49` | `FileExists` | D per-item check |  |
| 5 | `internal/metadata/cover.go:74` | `downloadCoverArtWithClient` | D per-item check |  |
| 5 | `internal/metadata/enhanced.go:525` | `WriteM4BCustomTags` | D per-item check | T |
| 5 | `internal/metafetch/file_pipeline.go:152` | `ComputeTargetPaths` | D per-item check | T |
| 5 | `internal/operations/trigger_source.go:24` | `WithTriggerSource` | D per-item check | T |
| 5 | `internal/operations/trigger_source.go:39` | `IsManual` | D per-item check | T |
| 5 | `internal/operations/freshness/freshness.go:65` | `NewPebbleFreshness` | D per-item check |  |
| 5 | `internal/operations/opmode/dryrun.go:100` | `Preview` | D per-item check |  |
| 5 | `internal/server/search_index_testing.go:16` | `Server.setSearchIndex` | D per-item check |  |
| 5 | `internal/server/middleware/ratelimit.go:231` | `IPRateLimiter.SweepCount` | D per-item check |  |
| 5 | `internal/util/perms.go:26` | `CreateFile` | D per-item check |  |
| 4 | `internal/ai/llm_scorer.go:43` | `NewLLMScorerWithBackend` | D per-item check |  |
| 4 | `internal/applygate/manual_only.go:28` | `FoldUnderscores` | D per-item check | T |
| 4 | `internal/database/book_own_folder.go:83` | `OwnFolderSplit.Spans` | D per-item check |  |
| 4 | `internal/database/catalog_entry_store.go:620` | `CatalogStore.GetRaw` | D per-item check |  |
| 4 | `internal/database/chromem_embedding_store.go:215` | `MetaBool` | D per-item check | T |
| 4 | `internal/httputil/types.go:75` | `NewListResponse` | D per-item check |  |
| 4 | `internal/httputil/types.go:80` | `NewListResponseWithTotal` | D per-item check |  |
| 4 | `internal/httputil/types.go:99` | `NewMessageResponse` | D per-item check |  |
| 4 | `internal/httputil/types.go:104` | `NewStatusResponse` | D per-item check |  |
| 4 | `internal/logger/operation.go:63` | `OperationLogger.SetMinDBLevel` | D per-item check |  |
| 4 | `internal/logger/operation.go:68` | `OperationLogger.SetCanceled` | D per-item check |  |
| 4 | `internal/logger/operation.go:132` | `OperationLogger.IsCanceled` | D per-item check |  |
| 4 | `internal/logger/operation.go:137` | `OperationLogger.OperationID` | D per-item check |  |
| 4 | `internal/metadata/assemble.go:42` | `AssembleBookMetadata` | D per-item check |  |
| 4 | `internal/metadata/metadata.go:36` | `SetMetadataExtractor` | D per-item check |  |
| 4 | `internal/metadata/providerhttp/providerhttp.go:265` | `newTestLimiter` | D per-item check |  |
| 4 | `internal/openlibrary/downloader.go:32` | `DumpURL` | D per-item check |  |
| 4 | `internal/operations/state.go:202` | `SaveRawParams` | D per-item check | T |
| 4 | `internal/operations/freshness/freshness.go:97` | `PebbleFreshness.Stamp` | D per-item check |  |
| 4 | `internal/organizer/apply_failure.go:262` | `LoadApplyRenameFailure` | D per-item check |  |
| 4 | `internal/pathutil/abbreviate.go:63` | `AbbreviatePath` | D per-item check | T |
| 4 | `internal/scanner/scanner.go:182` | `SetScanner` | D per-item check |  |
| 4 | `internal/server/undo_engine.go:24` | `PreflightUndoConflicts` | D per-item check |  |
| 4 | `internal/util/normalize.go:16` | `NormalizePath` | D per-item check |  |
| 3 | `internal/audiobooks/revert.go:635` | `RevertService.revertChange` | D per-item check |  |
| 3 | `internal/metadata/google_quota.go:86` | `IsDailyBudgetSpent` | D per-item check |  |
| 3 | `internal/operations/freshness/freshness.go:69` | `itemKey` | D per-item check |  |
| 3 | `internal/operations/freshness/freshness.go:73` | `itemPrefix` | D per-item check |  |
| 3 | `internal/organizer/service.go:1428` | `Service.organizeBooks` | D per-item check |  |
| 3 | `internal/repairs/guards.go:588` | `guardBooks` | D per-item check |  |
| 3 | `internal/repairs/owner.go:139` | `OwnerApproval.has` | D per-item check | T |
| 3 | `internal/server/search_reconciler.go:124` | `SearchIndexDroppedCount` | D per-item check |  |
| 3 | `internal/server/handlers/auth.go:236` | `IsHTTPSRequest` | D per-item check | T |
| 3 | `internal/server/handlers/abs/cache_refresh.go:59` | `cacheRefresher.wait` | D per-item check |  |
| 2 | `internal/aidispatch/capabilities.go:47` | `SharedFSFeature` | D per-item check |  |
| 2 | `internal/aidispatch/dispatch.go:111` | `WithSlots` | D per-item check |  |
| 2 | `internal/aidispatch/dispatch.go:114` | `WithHealth` | D per-item check |  |
| 2 | `internal/aidispatch/dispatch.go:117` | `WithAttribution` | D per-item check |  |
| 2 | `internal/applycap/applycap.go:122` | `Counter.Admitted` | D per-item check |  |
| 2 | `internal/audioext/audioext.go:145` | `Set.Has` | D per-item check |  |
| 2 | `internal/metadata/throttle.go:68` | `DurationFor` | D per-item check | T |
| 2 | `internal/versionprimary/rank.go:462` | `TierName` | D per-item check | T |
| 1 | `internal/authority/authority.go:193` | `pubOverrideKey` | D per-item check |  |
| 1 | `internal/logger/operation.go:89` | `OperationLogger.Trace` | D per-item check |  |
| 1 | `internal/logger/operation.go:90` | `OperationLogger.Debug` | D per-item check |  |
| 1 | `internal/logger/operation.go:91` | `OperationLogger.Info` | D per-item check |  |
| 1 | `internal/logger/operation.go:92` | `OperationLogger.Warn` | D per-item check |  |
| 1 | `internal/logger/operation.go:93` | `OperationLogger.Error` | D per-item check |  |
| 1 | `internal/plugins/maintenance/fragment_consolidation_fixer.go:4512` | `fragTitleID.sameWork` | D per-item check |  |
| 1 | `internal/server/middleware/absauth.go:134` | `ABSAuthError.Error` | D per-item check |  |
| 29 | `internal/database/pebble_store.go:444` | `NewPebbleStoreInMemory` | D test seam (keep) |  |
| 9 | `internal/database/book_row_iter.go:267` | `setRowScanFaultWithClose` | D test seam (keep) |  |
| 9 | `internal/database/chromem_embedding_store.go:79` | `NewInMemoryChromemStore` | D test seam (keep) |  |
| 9 | `internal/server/absauth/throttle.go:75` | `Throttle.SetSleep` | D test seam (keep) |  |
| 9 | `internal/server/absauth/throttle.go:85` | `Throttle.SetClock` | D test seam (keep) |  |
| 7 | `internal/authorcredit/authorcredit.go:212` | `ResetTitleCache` | D test seam (keep) |  |
| 7 | `internal/server/handlers/abs/handler.go:564` | `Handler.SetClock` | D test seam (keep) |  |
| 6 | `internal/aidispatch/health.go:46` | `newHealthWithClock` | D test seam (keep) |  |
| 6 | `internal/metadata/dailyquota/dailyquota.go:192` | `DailyBudget.SetClock` | D test seam (keep) |  |
| 6 | `internal/server/file_io_pool.go:116` | `SetGlobalFileIOPool` | D test seam (keep) | T |
| 5 | `internal/database/book_row_iter.go:258` | `setRowScanFault` | D test seam (keep) |  |
| 2 | `internal/server/handlers/abs/handler.go:561` | `Handler.SetSleep` | D test seam (keep) |  |
