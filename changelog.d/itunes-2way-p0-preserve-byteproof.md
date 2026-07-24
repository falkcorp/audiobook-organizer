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

Proven on the real 97,999-track library across both layers that transform bytes: the
**mutation** layer (`UpdateMetadataLE`/`RemoveTracksByPIDLE`) and the **encode** layer
(`WriteITLBytes` → header regeneration + recompress + re-encrypt). A relocate of 300 tracks
changed **only** the location pair; the full mith header (bookmark position, play count,
rating, dates) and every other atom byte-identical; 97,669–97,699 untouched tracks
byte-identical. The bookmark/field-preservation claim (design §INV-F2) is now proven, not
assumed.

The proof also surfaced a **P2 blocker (F7)**: the `location-form` safety guard rejects the
entire live AO library (82,976 tracks) because its media legitimately lives under
`.itunes-writeback/iTunes Media/` (iTunes is pointed at the AO library there) and the guard
treats any `.itunes-writeback/` substring as a staging-dir leak. The relocate op cannot write
the library until this is reconciled (scope the staging-marker check to the write target). See
`docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F6–F7.
