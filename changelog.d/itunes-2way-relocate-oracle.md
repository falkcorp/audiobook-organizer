<!-- file: changelog.d/itunes-2way-relocate-oracle.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d9c1e73-8b52-4a60-9f18-2c7b5a0e3d61 -->
<!-- last-edited: 2026-07-24 -->

### Added

#### iTunes 2-way-sync — relocate acceptance oracle (auto-rollback trigger, not yet wired)

`internal/itunes/relocate_oracle.go` adds `VerifyRelocateWrite(before, after,
relocatedPIDs)`, the post-write acceptance oracle the P2 decoupled write cycle uses to
gate an atomic rename / trigger auto-rollback. It compares the decompressed ITL payload
before vs after at the per-track **raw-byte** level and confirms the write did exactly
what was planned: every relocated PID changed only its location pair (0x0D/0x0B), every
other track is byte-identical, and no track was added or removed. Raw-byte comparison
catches any unintended change — including atoms the LE parser does not decode (resume
bookmark, artwork, sort keys) — which a parsed-field diff cannot see.

Proven on the real 97,999-track library (env-gated test): a genuine 300-track relocate
verifies clean (300 relocated, 97,699 untouched byte-identical) and a single tampered
byte in an untouched track is caught. Purely additive and fully unit-tested (happy
relocate, undeclared change, non-location mutation, track removal, idempotence). Nothing
calls it yet; the P2 sync cycle wires it in a later PR. Independent of the F7 blocker.
