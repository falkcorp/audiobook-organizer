### Removed

#### Unreachable helpers in fileops, audioutil, audioext, mediainfo and pathutil

Eighteen functions that no binary can reach are deleted together with the tests
that only exercised them: `audioext.Set.Has`, `audioutil.DetectDRM`,
`FFprobeAvailable`, `MediaInfoAvailable`, `DurationProbeAvailable`,
`DurationProbeStats`, `CumulativeOffsets`, `ShiftChapters`,
`fileops.CopyFileIngest`, `CopyFileAtomic`, `GetFileSize`,
`FileOperation.Rollback`, `SafeMove`, `VerifyFileIntegrity`,
`mediainfo.GetQualityTier`, `pathutil.Abbreviate`, `AbbreviatePath` and
`CommonDir`. State that only they used is removed with them (the mediainfo
hit and fallback counters, the `modeKeepDest` copy policy and the separate
mode-path argument of the copy helper). Runtime behaviour does not change.
