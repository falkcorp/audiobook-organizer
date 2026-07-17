<!-- file: docs/specs/fable5-spec-itunes-writeback-hardening.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7a1c4e9b-2d5f-4a8c-b3e6-0f1a2b3c4d5e -->

# SPEC 2: iTunes Library Writeback Hardening

Goal: rock-solid `.itl` writeback. iTunes opens semi-broken libraries; **Apple Devices
(Windows iPhone sync) crashes on corrupt tracks** — and iTunes renames a library it
distrusts to "(Damaged)" and rebuilds from backup, losing whatever the writeback intended.
This spec defines a formal safety contract, an atomic write protocol, and a regression
suite, all derived from binary forensics of four real damaged libraries (see
`fable5-review-findings.md` for the full evidence tables; finding IDs referenced below).

## 1. Damaged Library Analysis (empirical summary)

Method: built and ran `cmd/itl-check`, `cmd/itl-diff`, `cmd/itl-repair` against
`/tmp/itunes-libraries/{golden, writeback, damaged-1..4}`; then decrypted/inflated payloads
(AES-128-ECB prefix + zlib, mirroring `parseHdfmHeader → itlDecrypt → itlInflate`) and
walked every msdh container, every mith/mhoh/miph/mtph chunk.

### What each tool can and cannot detect (mandated statement)

| Tool / guard | Detects | Blind to |
|---|---|---|
| `cmd/itl-check` | parse-level track/playlist counts | everything structural; its "Tracks with Location" counter is broken (HIGH-2) |
| `cmd/itl-diff` | hdfm header hex diff; per-PID numeric fields (Size, TotalTime, BitRate, …); track add/remove sets | playlist membership (`mtph`/`miph`); **all string fields** (Name/Album/Artist/Genre/Kind/Location are never parsed on LE — HIGH-2); mhoh-level encoding; msdh inventory (docstring claims it; not implemented — MED-5) |
| `cmd/itl-repair` | dangling `mtph` items (and removes them) | header count desync; mhoh format violations; everything else |
| `VerifyITLNoNewDanglingRefsLE` / `VerifyITLNoDanglingRefsLE` | playlist→track dangling refs (LE only) | all other classes; **fails open** when master list unlocatable (MED-7); silently skips BE (LOW-2) |
| `ValidateITL` | header decrypts, track count nonzero | structure entirely (MED-6) |

### Corruption classes found, with provenance

| # | Class | Status | Evidence | Current code |
|---|---|---|---|---|
| K1 | Dangling `mtph` playlist refs after track removal | **OBSERVED** | damaged-1: 6 items across 3 `miph` parents | guarded (`VerifyITLNoNewDanglingRefsLE`); `RemoveTracksByPIDLE` v1.2.0 removes orphans in-pass |
| K2 | `hdfm` header count fields stale vs payload (tracks @0x44; albums @0x4C; artists @0x54; playlists @0x48 — BE, inside `headerRemainder`) | **OBSERVED** | damaged-1/2: header 90,900 vs payload 90,898 | **unguarded, live** (CRIT-3). No mutation path touches the header remainder |
| K3 | mhoh encoding-flag byte `+27` set to 1/3 (iTunes writes only 0x00; its encoding indicator is at `+24`) | **OBSERVED** | golden: 0 of 281,790 blocks; damaged-1: 251K blocks; damaged-3: 173K; damaged-4: 102 | **unguarded, live** (CRIT-1) via `encodeHohmString` |
| K4 | Location 0x0D holds `file://` URL; iTunes format is native Windows path (URL belongs in 0x0B only) | **OBSERVED** | damaged-1/3: 83,783 blocks; damaged-4: 34 (incl. paths into `.itunes-writeback/` staging) | **unguarded, live** (CRIT-2) |
| K5 | mhoh `headerLen` written as totalLen (legal value: 24, golden is 100% uniform) | **OBSERVED** | damaged-1: ~60K type-0x02 blocks (headerLen 41–210); damaged-3: ~3.4K; damaged-4: ~33 | writer fixed (`mhohFixedHeaderLen`, regression test exists); **no read-time detector** (HIGH-6) |
| K6 | Invalid TID sequences/gaps | SPECULATIVE | golden+damaged all have sorted, duplicate-free TIDs (verified) | not needed as a primary guard; cheap to assert |
| K7 | Malformed mhoh length prefixes (totalLen ≠ 40+strLen) | SPECULATIVE | zero instances in all six libraries | include in contract anyway — trivially cheap |
| K8 | Playlist sort fields (`mnol`/`mpsl`) pointing at deleted tracks | SPECULATIVE | tags not present in these libraries' containers; per-miph declared-vs-actual counts all consistent (335/335 playlists, all match) | covered indirectly by K1 guard |
| K9 | PID collisions / format violations | SPECULATIVE | no duplicates observed | cheap assert in contract |
| K10 | Smart-playlist criteria referencing removed fields | SPECULATIVE | smart data (`msph`/type-21 container) byte-identical across libraries | out of contract v1 |
| K11 | mith ordering requirements | SPECULATIVE (golden is TID-sorted; damaged too) | preserve-order rule in contract | |
| K12 | BE writer parity | N/A here (all evidence LE) | **refuse BE writes** rather than replicate guards (LOW-2) | |

### Which corruption breaks Apple Devices specifically?

Not directly testable from this machine (Apple Devices is a Windows app). What the evidence
supports: iTunes' own damage detector trips on **per-block format violations** (damaged-4
was rejected with only ~34 K3+K4 blocks and *no* other inconsistency we could find), so the
crash-causing classes are, in priority order, K3/K5 (string blocks whose declared layout
disagrees with iTunes' reader — a parser in Apple Devices that trusts `headerLen` or the
`+24` encoding indicator will read garbage or out-of-bounds) and K4 (path field containing
a URL — any code that feeds 0x0D to Win32 file APIs gets an invalid path). The
compatibility checklist (§5) therefore requires byte-pattern conformance with
iTunes-authored blocks, not merely self-consistency. Marked as inference, not observation.

## 1b. NORMATIVE: the Location field contract (0x0B / 0x0D)

This section is the single source of truth for how location fields are written. It was
census-verified against the untouched library `itunes-lib-good.itl` (SHA-256-identical to
golden; 93,014 local tracks + 1,187 podcast entries, zero exceptions found). Any code or
doc that contradicts it is wrong.

**One source of truth, two derived renderings.** A track's location is a single Windows
absolute path. It is rendered into the .itl twice:

| Field | hohm type | Content | Example |
|---|---|---|---|
| Location | `0x0D` | The plain Windows path. Backslashes. **No** `file://`. **No** percent-escaping. | `W:\itunes\iTunes Media\Audiobooks\Adrian Tchaikovsky\01 Children of Time - 1.mp3` |
| LocalURL | `0x0B` | `file://localhost/` + the same path with `\`→`/` and RFC-3986 percent-escaping | `file://localhost/W:/itunes/iTunes%20Media/Audiobooks/Adrian%20Tchaikovsky/01%20Children%20of%20Time%20-%201.mp3` |

Rules (all empirically grounded, zero counter-examples in 187,215 censused blocks):

1. **Never write a URL into 0x0D.** Never write a bare path into 0x0B. The two fields are
   derived from one `LocationPair` value (TASK-006); no caller may pass a raw string to
   either field directly.
2. **Pairing:** every track with a local file has BOTH fields, and they round-trip:
   `FromWinPath(p).URL == sibling 0x0B` and `FromURL(u).WinPath == sibling 0x0D`.
3. **Podcast/stream tracks have no 0x0D at all** and carry an `http(s)://` URL in 0x0B
   (1,187 in the census). Writeback must never add a 0x0D to them, and validators must
   not require one.
4. **Encoding is orthogonal to content.** Non-ASCII paths are stored UTF-16-encoded
   (1,736 of 93,014 in the census) with the block's encoding signaled per CRIT-1's
   corpus rules — writing the correct *string* with wrong encoding bytes is still
   corruption. TASK-005 (encoders) and TASK-006 (LocationPair) must both land before
   writeback is considered safe.
5. The familiar `file://localhost/W:/...` form seen in `iTunes Library.xml` exports is
   the XML rendering of the location — it corresponds to 0x0B, never to 0x0D.

**The historical bug in one sentence:** callers passed URL-shaped `f.ITunesPath` and the
writer copied it into 0x0D verbatim, so both fields held the URL form (83,783 blocks in
damaged-1/3; 34 in damaged-4) — which iTunes rejects.

## 2. `ITLSafetyContract` (write-guard contract)

A single Go type (new file `internal/itunes/itl_safety_contract.go`) that runs an ordered
list of named, individually testable guards over `(before, after []byte)` decompressed LE
payloads + the proposed new `hdfm` header. **All guards must pass before any byte reaches
disk.** Guards are pure functions; each returns a structured violation list (guard name,
offset, chunk tag, message), never just a bool — auditability is part of the contract.

```
type GuardResult struct {
    Guard      string // stable name, e.g. "mhoh-format"
    Violations []Violation // empty = pass
}
type Violation struct {
    Offset  int
    Chunk   string // "mhoh", "mith", ...
    Message string
}
ContractVerdict { Pass bool; Results []GuardResult; before/after summary counts }
```

Guards (v1, ordered cheapest-first; names are normative for tests):

| Guard name | Asserts | Catches |
|---|---|---|
| `parse-roundtrip` | `after` decrypts/inflates/parses; master list locatable; **fail closed** on any parse surprise (inverts today's fail-open) | MED-7, gross corruption |
| `container-tiling` | msdh containers tile the payload exactly; every container's totalLen sums with content; child walk reaches contentEnd with 0 gap in types 1 and 2 | truncation/splice errors |
| `count-coherence` | `mlth` count == actual mith blocks == proposed header @0x44; playlist count @0x48 == miph count; album @0x4C == miah count; artist @0x54 == miih count; every `miph` declared item count == actual mtph children | **K2**, K8 |
| `no-new-dangling-refs` | existing `VerifyITLNoNewDanglingRefsLE`, converted to fail-closed | K1 |
| `mhoh-format` | every mhoh: headerLen == 24; totalLen == 40 + strDataLen; strDataLen bound-checked; byte `+27` == 0x00; bytes `+32..+39` zero; `+24` value ∈ the set observed in iTunes-authored blocks for that hohm type (corpus-derived constant table) | **K3, K5, K7** |
| `location-form` | per-track, on **decoded** strings (0x0D may be UTF-16-encoded — 1,736 of golden's 93,014 are): if a track has a 0x0D, it must parse as a Windows absolute path (drive letter, backslashes, no `file://`, no `%`-escapes) and its sibling 0x0B must be a `file://localhost/` URL with RFC-3986 escaping that round-trips to the 0x0D path; tracks **without** 0x0D (podcast/stream entries — 1,187 in golden) may carry any `http(s)://` URL in 0x0B and are exempt from the pairing rule; **no value contains `.itunes-writeback/`** or other staging markers. Census basis: golden 0x0D = 93,014 blocks, zero `file://`; 0x0B = 94,201 blocks (93,014 `file://localhost/` + 1,187 `https://` podcast) | **K4** + staging leak |
| `tid-pid-sanity` | TIDs sorted ascending, unique; PIDs unique, nonzero | K6, K9, K11 |
| `bounded-delta` | guardrail: a single writeback may not remove > N tracks (config, default 5,000) nor rewrite > M% of mhoh blocks (config, default 20%) without an explicit `force` flag | blast-radius cap for HIGH-3-style bugs |

Notes:
- `mhoh-format`'s `+24` allowed-value table must be **derived from the golden corpus by a
  one-off audit tool**, not hand-invented (Task plan: ITW-1). Until derived, the guard
  enforces only "+27 == 0" + headerLen + length arithmetic, which already catches every
  observed instance of K3/K5.
- Guards run on the *decompressed payload + proposed header pair*, so K2 is checkable
  before encryption/compression.
- The contract API also exposes `AuditITL(data []byte)` (single-library mode, `before ==
  nil`) for use by `cmd/itl-check` and a new maintenance endpoint, so already-corrupt
  libraries (K5 carriers) are detectable at read time — closing HIGH-6.

## 3. Atomic write protocol

New single chokepoint `SafeWriteITL(path string, mutate func(payload []byte) ([]byte, error), opts ...)` in
`internal/itunes/itl_safe_write.go`. **All writeback paths (`itl_combined_mutate.go`,
`UpdateITLLocations`, rebuild, service batcher) are refactored to go through it.** Protocol:

1. Read original; parse; snapshot `before` payload + header. Refuse BE (K12).
2. Apply `mutate` to a copy. Recompute header count fields (fixes CRIT-3 by construction:
   header remainder is *regenerated*, with @0x44/0x48/0x4C/0x54 patched from actual payload
   counts, all other remainder bytes preserved).
3. Run `ITLSafetyContract(before, after, newHeader)`. Any violation → delete nothing,
   write nothing, return the structured verdict in the error. Log full verdict at ERROR
   with op-id tags (per repo logging discipline).
4. Encode (encrypt+deflate), write to `<path>.itl.new` in the same directory (same
   filesystem → atomic rename), fsync file.
5. Re-read `<path>.itl.new` from disk, decode, and run the contract **again** against the
   re-read bytes (catches encode-path bugs — the BestSpeed zlib + AES boundary code is
   itself a historic risk area). 
6. Backup: rename `<path>` → `<path>.bak-<RFC3339>`; then rename `<path>.itl.new` → `<path>`;
   fsync directory.
7. On any failure after step 4: remove `.itl.new`, original untouched.
8. **Lock discipline:** refuse to write if iTunes/Apple Devices may have the file open.
   On the remote-Windows deployment this means the writeback service must check the
   configured "iTunes running" signal (existing sync service heartbeat) and the contract
   gains a precondition `library-not-in-use`. (A library replaced under a running iTunes is
   a plausible non-structural cause of "(Damaged)" renames for internally-consistent files
   like damaged-3/4 — we could not falsify structural causes for those two beyond K3/K4/K5
   counts, so both protections are mandatory. Stated as inference.)

**Backup retention policy:** keep the most recent **10** `.bak-*` per library *plus* one
"last-known-good" pin (`.bak-lkg`, updated only after a subsequent successful iTunes open
is confirmed via the sync service), TTL 90 days for unpinned baks, cleanup in the existing
maintenance loop alongside `.bak-*` copy-on-write cleanup. Rationale: damage is often
discovered many syncs later (the four damaged files span weeks), so a deep history matters
more than disk (30MB × 10 = 300MB, negligible on the ZFS pool).

## 4. Apple Devices compatibility checklist (golden-derived invariants)

Every writeback output MUST satisfy (all verified true of the golden library, all violated
by at least one damaged library unless noted):

1. Header @0x44/0x48/0x4C/0x54 equal actual mith/miph/miah/miih counts; header fileLen
   field @0x08 equals on-disk size.
2. Every mhoh: headerLen == 24; totalLen == 40 + strDataLen; bytes 32–39 zero; byte +27 == 0.
3. mhoh `+24` encoding indicator ∈ corpus-derived set per hohm type.
4. 0x0D (when present) = absolute Windows path, never `file://`; its 0x0B sibling =
   `file://localhost/` percent-escaped URL, pairwise consistent. Tracks without a local
   file (podcasts) have no 0x0D and may hold `http(s)://` in 0x0B. Verified by full
   census of the untouched library (`itunes-lib-good.itl`, SHA-256-identical to golden):
   93,014 0x0D blocks, zero `file://`; 91,278 ASCII + 1,736 UTF-16-encoded backslash paths.
5. No `mtph` referencing a TID absent from the master list; per-miph declared count ==
   actual mtph children.
6. TIDs strictly ascending and unique in the master list; PIDs unique (golden-verified;
   speculative as a *requirement*, cheap to keep).
7. msdh containers tile the file exactly; all 15 container types present in the same order
   as the source library (we preserve order by construction — splice-in-place mutations).
8. Payload inflates below the configured cap with fail-closed behavior at the cap.

## 5. Safeguard gaps → implementations (each is a plan task)

| Gap | Implementation | Plan task |
|---|---|---|
| No corpus-derived mhoh encoding table | one-off audit tool reads golden, emits Go constant table + JSON snapshot under `internal/itunes/testdata/` | ITW-1 |
| K3: writer emits foreign flag bytes | rewrite `encodeHohmString`/`buildMhohLE`/`rewriteHohmLocationLE` to emit iTunes-conformant `+24/+27` bytes per corpus; never invent flags | ITW-2 |
| K4: 0x0D/0x0B form confusion | central `LocationPair{WinPath, URL}` type with normalization + escaping; all writers take it; guard `location-form` | ITW-3 |
| K2: stale header counts | header regeneration in SafeWriteITL step 2 | ITW-4 |
| No safety contract | `ITLSafetyContract` per §2 | ITW-5 |
| No atomic write | `SafeWriteITL` per §3 + refactor all writers onto it | ITW-6 |
| HIGH-2: vacuous LE string parse | fix `walkMsdhTracksLE` to descend into mith children (walk `offset+headerLen .. offset+totalLen`); unblocks real diffing | ITW-7 |
| Tools lie (MED-5) | `itl-diff`: add msdh inventory + playlist-membership diff + mhoh-format audit; `itl-check`: run `AuditITL` | ITW-8 |
| HIGH-3: full-library rewrite each sync | diff-before-write in writeback_batcher (skip ITLMetadataUpdate when values unchanged — requires ITW-7's parser fix to read current values) | ITW-9 |
| Fail-open verifier (MED-7) | fail-closed conversions inside contract | ITW-5 |
| BE writes unguarded (LOW-2) | `SafeWriteITL` refuses BE | ITW-6 |
| Already-corrupt libraries undetectable (HIGH-6) | `AuditITL` + maintenance/diagnostics endpoint | ITW-8 |

## 6. Regression test suite design

Location: `internal/itunes/itl_safety_contract_test.go` + extend `generate_test_itls.go`.
Pattern per test: generate minimal valid LE .itl (existing generator) → apply a *specific
corrupting mutation* with test-local helpers (kept in `_test.go` so production code never
contains corruptors) → assert the named guard catches it, and that the *other* guards
don't false-positive on the valid base. All tests idempotent, no real iTunes, no /tmp
fixtures (the damaged libraries are ephemeral evidence, not test inputs — but their
*signatures* are encoded in these mutations).

| Test | Mutation | Must be caught by |
|---|---|---|
| `TestContract_DanglingMtph` | excise mith, keep mtph (use preserved `removeTracksByPIDLEUnsafe`) | `no-new-dangling-refs` |
| `TestContract_HeaderCountDesync` | remove a track, keep old header | `count-coherence` |
| `TestContract_MhohForeignFlag` | set byte+27=3 on one mhoh | `mhoh-format` |
| `TestContract_MhohHeaderLen` | set headerLen=totalLen on one mhoh | `mhoh-format` |
| `TestContract_MhohLenArithmetic` | totalLen ≠ 40+strLen | `mhoh-format` |
| `TestContract_LocationURLIn0x0D` | write URL into 0x0D | `location-form` |
| `TestContract_StagingPathLeak` | location containing `.itunes-writeback/` | `location-form` |
| `TestContract_MiphCountMismatch` | decrement a miph declared count | `count-coherence` |
| `TestContract_TidDuplicate` / `_Unsorted` | duplicate / swap TIDs | `tid-pid-sanity` |
| `TestContract_TruncatedContainer` | shrink an msdh totalLen | `container-tiling` |
| `TestContract_FailClosedOnUnparseable` | corrupt master-list msdh tag | `parse-roundtrip` |
| `TestContract_BoundedDelta` | remove 5,001 tracks | `bounded-delta` |
| `TestContract_CleanPasses` | no mutation | all guards pass |
| `TestSafeWrite_RollbackOnViolation` | mutate func introduces K2 | original byte-identical after call; no `.itl.new` left |
| `TestSafeWrite_BackupRotation` | 12 successive writes | 10 baks + lkg retained |
| `TestParseLE_TrackStrings` (ITW-7) | golden-shaped fixture | Name/Location populated |

CI: all of the above run under `make test` / `make ci`; no network, no fixtures outside
testdata.
