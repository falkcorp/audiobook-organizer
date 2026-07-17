<!-- file: docs/specs/2026-07-03-itl-identity-and-external-truth-hardening.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6c2e8f4a-1b7d-4e9c-a3f5-8d0b1c2e3f4a -->
<!-- last-edited: 2026-07-03 -->

# SPEC 3: iTunes Library Identity & External-Truth Hardening

Goal: close the class of failures the June 2026 contract (SPEC 2) structurally
cannot see — **semantically-valid wrong content**. The SPEC 2 guards certify
"this is a well-formed iTunes library"; nothing certifies "this is *our*
library at *plausible* size". This spec adds the missing external truth
anchors, closes the known guard bypasses, and fixes the decoder defect that
makes one guard permanently noisy.

## 1. Motivating evidence: the July 2026 374-track cloud stub

Observed at `/mnt/bigdata/books/audiobook-organizer/.itunes-writeback/iTunes
Library.itl` (2026-07-01, 1,058,546 bytes; every `.bak` since 2026-06-21
byte-identical). `itl-check`: **all 8 guards PASS.** `itl-diff` vs the golden
master (90,900 tracks):

| Property | Golden | Stub |
|---|---|---|
| Tracks | 90,900 | 374 |
| Playlists | 335 | 14 |
| Tracks with Location | 89,339 | **0** |
| Library Persistent ID @0x34 | `48E87F59865568B0` | `48E87F59865568B0` (same) |
| Shared track PIDs | — | **0 of 374** |
| Content | audiobooks + music | cloud-only music entries |

Provenance (operator-confirmed): iTunes on the Windows box failed to load its
library, created a fresh one, and populated it from the account's cloud music
list; the stub was then deliberately copied into the staging dir as a safe
test baseline. The point stands regardless of intent: **a different library
with 0.4% of the tracks, at the same path, with the same Library PID, passes
every guard.** iTunes is a co-writer of this file and the contract only
models our own writes.

### Why each guard passed (the vacuity map)

- `count-coherence` — self-referential: `regenerateHeaderCounts` rewrites the
  header from the same payload the guard then compares it to; equal by
  construction. It can catch internal desync, never a wholesale shrink.
- `location-form` — a track with no `0x0D` block returns early; at 0 locations
  the guard passes vacuously on every track.
- `bounded-delta` — the only magnitude guard, bypassed three ways:
  (a) `before == nil` (all audit-mode use, incl. `cmd/itl-check`),
  (b) `cfg.Force` — passed **unconditionally** by `RebuildITLFromDB` and
  `BuildExportITL`, the two paths most capable of mass loss,
  (c) it bounds only *removals*, so remove-all-then-add-few replacement
  passes even without Force.
- Everything else is structural and the stub is internally coherent.

### Compounding exposures found in the same audit

- `PinLastKnownGood` / `.bak-lkg` — **no production caller**; rotation
  (keep 10) had already reduced every backup to a copy of the stub.
- `SetLibraryNotInUse` / `WithLibraryNotInUse` — **no production caller**;
  the "iTunes may have it open" gate always WARN-and-proceeds.
- The batcher flush swallows `SafeWriteITL` errors as `slog.Warn` (no retry,
  no alert); its parse-failure branch logs WARN and proceeds to write.
- Two divergent write wrappers: `itunesservice.SafeWriteITL` (weak
  `ValidateITL` checks, own 5-backup scheme) vs the hardened
  `itunes.SafeWriteITL` (8-step protocol).
- `decodeMhohBlock` misclassifies string encodings on iTunes-authored
  libraries **in both directions** (ASCII decoded as UTF-16LE → CJK mojibake;
  UTF-16LE decoded as 8-bit), which makes `location-form` report ~2×
  tracks-with-location violations on **every known-good library including the
  golden master**. A guard that is always red on good data has effectively
  never been exercised.

## 2. New corruption/loss classes

Extending SPEC 2 §1's K-table:

| # | Class | Status | Evidence | Disposition |
|---|---|---|---|---|
| K13 | Same path, different library (library swapped underneath the writer — by iTunes rebuild, wrong file copied in, or population-replacing mutation). Library PID is NOT sufficient identity: the stub kept it with zero track-PID overlap. | **OBSERVED** (2026-07, benign instance) | stub vs golden: 0/374 shared track PIDs, same Library PID | `library-identity` guard (§3), **implemented v1** |
| K14 | Structurally-perfect library at implausible magnitude (rebuild from under-populated DB; fresh iTunes library; truncated import) | **OBSERVED** (same instance: 99.6% shrink, all guards green) | stub: 374 vs 90,900 | `expected-magnitude` guard (§3), **implemented v1** |
| K15 | Guard bypass via unconditional `Force` on rebuild/export paths | LATENT | `rebuild.go` passes `ForceContractConfig()` always | §5, planned |
| K16 | mhoh encoding misclassification at read time (decode-side twin of K3; corrupts strings if any path round-trips decode→encode; renders `location-form` permanently noisy) | **OBSERVED** (display/audit level) | golden master "fails" location-form with byte-swap mojibake; violation count = 2× tracks-with-location | §6, planned |

## 3. NORMATIVE: the identity fingerprint (K13/K14, implemented)

New module `internal/itunes/itl_identity.go`; guards registered in
`orderedGuards()` between `tid-pid-sanity` and `bounded-delta`.

**Sidecar.** After every successful `SafeWriteITL`, the bytes that actually
landed on disk (step-5 re-read payload + header) are fingerprinted into
`<library>.itl.identity.json`:

```json
{
  "schema_version": 1,
  "library_pid": "48e87f59865568b0",
  "track_count": 90900,
  "playlist_count": 335,
  "sample_stride": 88,
  "pid_sample": ["<up to 1024 evenly-spaced track PIDs, payload order>"],
  "updated_at": "2026-07-03T00:00:00Z"
}
```

Sidecar write failure is WARN-only (the library write already landed; a stale
sidecar only makes the next check stricter). Sidecar **read** failure is a
hard error — a corrupt anchor must not degrade to "no anchor". A missing
sidecar is a legitimate first-run state (guard disarmed for that write, then
the sidecar is created).

**Guard `library-identity` (K13).** Armed when `cfg.ExpectedIdentity != nil`
(SafeWriteITL populates it from the sidecar unless the caller already set it).
Violations:
- proposed header's Library PID ≠ fingerprint's (when both present);
- `SampleOverlapPct(after)` < `cfg.IdentityMinOverlapPct` (default **90**);
- fingerprint unassessable (empty sample) — fail closed.

Bypass is `cfg.AdoptLibrary` only: an explicit per-write operator
acknowledgment ("this is intentionally a different library"), after which the
new population is fingerprinted. `Force` does **not** bypass identity.

**Guard `expected-magnitude` (K14).** Armed when
`cfg.ExpectedTrackCount > 0`; `after`'s track count must be within
`cfg.MagnitudeTolerancePct` (default **10**) of it. The expected count comes
from *outside* the file — the DB's synced-book census or the sidecar's
`track_count`. This is the non-self-referential complement to
`count-coherence`.

Against the motivating incident: the stub write is rejected by
`library-identity` (overlap 0% < 90%) even though the Library PID matches,
and by `expected-magnitude` once callers wire an expected count. Verified
end-to-end in `TestSafeWrite_IdentityLifecycle`.

## 4. Wire the dead-code safety machinery (planned)

- Call `PinLastKnownGood` after a write that (a) passed identity and (b) is
  followed by evidence iTunes opened the result (sentinel mtime advance), or
  via an explicit operator bless command. Without this, keep-10 rotation
  guarantees a stub eventually owns every backup slot.
- Wire `SetLibraryNotInUse` from the service's iTunes-running heartbeat.
- Route the batcher flush and `server/itl_rebuild.go` through
  `itunes.SafeWriteITL`; retire `itunesservice.SafeWriteITL` and its parallel
  backup scheme. Flush failures become metric + alert, not WARN.
- Batcher parse-failure branch: abort + re-enqueue, never proceed-and-write.

## 5. Close the Force bypasses (K15, planned)

- `RebuildITLFromDB` / `BuildExportITL`: `Force` becomes an explicit caller
  argument, not baked in; even under Force, `bounded-delta` runs in report
  mode so the log records "this write removes N tracks".
- `bounded-delta` becomes symmetric: bound |Δtracks| and the replacement
  ratio (fraction of `after` PIDs absent from `before`), not removals only.

## 6. Fix the decoder, then trust the guards (K16, planned)

- Diagnose `decodeMhohBlock`'s discriminator against the golden corpus
  (`cmd/itl-audit-encoding`); the observed both-directions mojibake says the
  `+27 == 0` branch applies the wrong table to common types.
- **Acceptance:** `AuditITL` on the golden master reports **0
  `location-form` violations.** Add the checked-in `testdata` golden to CI:
  any guard failing on known-good data fails the build.

## 7. Test matrix (v1 implemented portion)

| Test | Asserts |
|---|---|
| `TestExtractLibraryPIDHex` | PID read at 0x34; zero/absent → "" |
| `TestComputeLibraryIdentity` | counts, stride, sample; unparseable payload errors (never a vacuous anchor) |
| `TestSampleOverlapPct` | 100 on self, 0 on disjoint, −1 unassessable |
| `TestGuardLibraryIdentity` | disarmed / adopt / continuation / replaced / PID-change / fail-closed |
| `TestGuardExpectedMagnitude` | disarmed / exact / tolerance / 99.6%-shrink / growth |
| `TestIdentitySidecarRoundtrip` | missing→(nil,nil); roundtrip; corrupt→error |
| `TestSafeWrite_IdentityLifecycle` | fingerprint on first write; stub-class replacement rejected byte-identically; AdoptLibrary blesses and re-anchors |
