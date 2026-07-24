<!-- file: changelog.d/itunes-2way-p0-preserve-byteproof.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9c4e2a71-6b83-4d50-a1f7-3e8b5c0d2f64 -->
<!-- last-edited: 2026-07-24 -->

### Added

#### iTunes relocate/remove field-preservation byte-proof (env-gated test)

`internal/itunes/itl_preserve_proof_test.go` adds `TestITLPreservationByteProof`, a per-track
raw-byte comparison of the decompressed ITL payload before vs after a real relocate + remove
(`UpdateMetadataLE` + `RemoveTracksByPIDLE`). Because it compares raw bytes it catches any
change to any atom the parser ignores — including the audiobook resume bookmark, which the
binary parser does not parse. Env-gated (`ITL_PRESERVE_PROOF_PATH`); skips in CI.

Proven on the real 97,999-track library: a relocate of 300 tracks changed **only** the
location pair (0x0D/0x0B) — the full mith header (bookmark position, play count, rating,
dates) and every other atom (Comment, Sort Name/Artist, content advisory, audio-data blob)
byte-identical; a remove of 30 tracks touched only those; 97,669 untouched tracks
byte-identical (exact partition, zero collateral mutation). The bookmark/field-preservation
claim (design §INV-F2) is now proven, not assumed. See
`docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F6.
