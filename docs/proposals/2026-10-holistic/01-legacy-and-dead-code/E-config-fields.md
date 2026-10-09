<!-- file: docs/proposals/2026-10-holistic/01-legacy-and-dead-code/E-config-fields.md -->
<!-- version: 1.2.0 -->
<!-- guid: be66a781-5edd-495e-bb5f-04f7cbb79705 -->
<!-- last-edited: 2026-10-09 -->

# Appendix E: Config fields with no read outside internal/config

**Round-2 (r1, 2026-10-09).** Re-run at HEAD `ebda30d47` with `grep -rn '\.<Field>\b' internal cmd --include='*.go' | grep -v _test | grep -v internal/config/`: every field below still has 0 reads outside the package (the 110 `.Language` hits are `Book.Language`/provider structs, not `Config.Language`). The **Decision** column applies D15 and D14e. Note on `EmbedCoverArt`: the field is unread, but the cover-embed code runs unconditionally (`metafetch/service.go:652`), so "wire" means adding a gate and flipping the default (`config.go:3719`) to `true` in the same PR.

| field | decision (D15) | PR |
|---|---|---|
| `CreateBackups` | wire at `fileops.WriteTagsSafe` | 01 P79b |
| `VerifyAfterWrite` | wire at `tagger.WriteTagsSafe` | 01 P79c |
| `EmbedCoverArt` | gate `metafetch.embedCoverInBookFiles`; default → `true` | 01 P79a |
| `MetadataReviewDefaultView`, `DefaultUserQuotaGB`, `MemoryLimitType`, `CacheSize`, `MemoryLimitPercent`, `MemoryLimitMB`, `LogFormat`, `EnableJsonLogging`, `AuthRateLimitPerMinute`, `Language`, `APIKeys` | remove field, load, default, `persistence.go` case, `protected_fields.go:92`, Settings UI and the TS `Config` type | 01 P80 |

Method: a typed `go/packages` walk (scratch tool, `fieldusage`) counts every selector expression on `config.Config` across `./...` (non-test), split into reads outside `internal/config/`, reads inside it, and writes. 158 top-level fields; 21 have zero reads outside the package. Fields with in-package reads were each traced to a getter and the getter's callers outside `internal/config`. Live: `PathAliases` (seeded and validated, then used through path resolution), `ActivityDBPath` (`ResolveActivityDBPath`, 3 outside callers), `SetupComplete` (read by `web/src/App.tsx:82`), `AuthorityEvidenceEnabled` (passed as a func value at `internal/server/server.go:632`), `BootstrapKeyTTL`/`BootstrapKeyTTLDays` (`ResolveBootstrapKeyTTL`, `server/bootstrap.go:353`, `server/apikey_expiry_stamp.go:123`), `ChapterConsolidationThresholdMin` (`ResolveChapterConsolidationThresholdMin`, 2 callers). **Inert**: `AuthRateLimitPerMinute` (validated only) and `Language` (persisted and shown in Settings only). Those 2 plus the 12 with no read at all are the 14 in U4. JSON exposure to the Settings page is a display read, not a behavior gate.

| field | reads outside | reads inside | writes |
|---|---|---|---|
| `PathAliases` | 0 | 2 | 2 |
| `ActivityDBPath` | 0 | 1 | 1 |
| `SetupComplete` | 0 | 1 | 3 |
| `CreateBackups` | 0 | 0 | 1 |
| `DefaultUserQuotaGB` | 0 | 0 | 1 |
| `EmbedCoverArt` | 0 | 0 | 1 |
| `Language` | 0 | 2 | 1 |
| `MetadataReviewDefaultView` | 0 | 0 | 1 |
| `AuthorityEvidenceEnabled` | 0 | 1 | 0 |
| `ChapterConsolidationThresholdMin` | 0 | 4 | 1 |
| `AuthRateLimitPerMinute` | 0 | 1 | 1 |
| `MemoryLimitType` | 0 | 0 | 1 |
| `CacheSize` | 0 | 0 | 1 |
| `BootstrapKeyTTL` | 0 | 1 | 0 |
| `BootstrapKeyTTLDays` | 0 | 5 | 0 |
| `MemoryLimitPercent` | 0 | 0 | 1 |
| `MemoryLimitMB` | 0 | 0 | 1 |
| `LogFormat` | 0 | 0 | 1 |
| `EnableJsonLogging` | 0 | 0 | 1 |
| `APIKeys` | 0 | 0 | 0 |
| `VerifyAfterWrite` | 0 | 0 | 0 |
