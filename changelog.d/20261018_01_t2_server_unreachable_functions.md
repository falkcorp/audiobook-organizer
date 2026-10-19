Deleted 27 production functions in `internal/server`, `internal/server/middleware`
and the `handlers`, `handlers/abs` and `handlers/aibackends` packages that no
binary could reach. Eleven of them were helpers that only tests called
(`applyCachedCandidateForBook`, `setSearchIndex`, `pathLocks.setTrace`,
`SearchIndexDroppedCount`, the `server.PreflightUndoConflicts` re-export, `cacheRefresher.wait`, `Handler.SessionTimeListening`,
`aibackends.WithAttribution`, `IPRateLimiter.Len` and `SweepCount`, and the
`NewPlaylistHandler` constructor); they now live in `_test.go` files or the
tests call the live equivalent, so the server binary no longer carries them.
`IPRateLimiter.Stop` is gone along with its stop channel: the sweeper already
ends when its context is cancelled. The `validateAbsolutePath` tests now call
`pathvalidation.CleanAbsolutePath` directly, which had no tests of its own. The
duplicated `stringVal`, `intVal`, `decodeRawValue`, `stripChapterFromTitle` and
`stripSubtitle` copies in `internal/server` were removed with their tests, and
the `loadMetadataState` wrapper was removed (its two tests call
`metafetch.LoadStateSnapshot`); the live copies in `internal/audiobooks` and
`internal/metafetch` are unchanged. Running behavior does not change.
