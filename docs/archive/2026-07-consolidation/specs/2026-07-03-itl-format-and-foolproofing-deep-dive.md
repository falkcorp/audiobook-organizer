<!-- file: docs/specs/2026-07-03-itl-format-and-foolproofing-deep-dive.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e5d7c3b-2a4f-4d8e-b1c6-7f0a9b8c2d3e -->
<!-- last-edited: 2026-07-03 -->

# .itl Format & Writeback Foolproofing — Full Deep Dive (2026-07-03)

This is the complete record of the July 2026 deep dive: the byte-level format
as this codebase understands it, the empirical census of every library on the
server, the adversarial audit of the safety pipeline, the documented failure
history, and the full hardening roadmap. SPEC 3
(`2026-07-03-itl-identity-and-external-truth-hardening.md`) is the normative
subset that is being implemented; this document is the evidence base and the
everything-else. Companion docs: SPEC 2
(`fable5-spec-itunes-writeback-hardening.md`), findings
(`fable5-review-findings.md`), `docs/itl-binary-format.md`.

---

## Part A — The .itl binary format (as implemented in `internal/itunes`)

### A.1 Envelope: `hdfm` header (plaintext, big-endian)

`hdfmHeader` / `parseHdfmHeader` (`itl.go:419-484`):

| file offset | size | field |
|---|---|---|
| 0x00 | 4 | magic `"hdfm"` |
| 0x04 | 4 BE | `headerLen` (payload starts here) |
| 0x08 | 4 BE | `fileLen` |
| 0x0C | 4 BE | `unknown` — **opaque, preserved verbatim** |
| 0x10 | 1 | version-string length |
| 0x11 | N | version string (e.g. `"12.13.10.3"`) |
| 0x34 | 8 | **Library Persistent ID** (matches the Album Artwork cache dir name; empirical, added for K13) |
| 0x44/0x48/0x4C/0x54 | 4 BE each | track/playlist/album/artist counts (CRIT-3 fields) |
| 0x5C | 4 BE | `maxCryptSize` (read iff headerLen > 96) — AES boundary |
| 17+len(ver) → headerLen | rest | `headerRemainder` — **opaque blob**, only the 4 counts (+ nothing else) patched on write |

`headerFixedPrefix = 17`; a field at file offset F lives at remainder offset
`F − (17 + len(version))`.

### A.2 Encryption and compression

- **AES-128-ECB**, hardcoded key `"BHUILuilfghuila3"` (`itl.go:27`), block-wise,
  no IV. **Only a prefix is encrypted**: for version ≥ 10, `limit =
  maxCryptSize` if non-zero else capped at **102400 bytes**; floored to a
  16-byte multiple; the rest of the payload is cleartext (`itl.go:240-251`).
  Pre-v10 encrypts the whole payload.
- **zlib**: detected by leading `0x78`; absent → passthrough. Inflation is
  fail-closed (T010) with a 2 GiB cap. Deflation MUST use `zlib.BestSpeed`
  (level 1) — iTunes rejects Go's default level-6 output.
- Order: decrypt → inflate on read; deflate → encrypt on write
  (`parseITLData` `itl.go:516-548`, `encodeITLPayload` `itl.go:967-981`).

### A.3 Two chunk vocabularies (LE vs BE)

`detectLE` (`itl.go:176`): payload starting with `"msdh"` → LE; else BE.

- **LE (v10+, all production)**: `msdh` section containers with `blockType`
  at +12 (1=tracks, 2=playlists, 9=albums, 11=artists; also 4, 12–16, 19–23
  observed, semantics unknown). Inside: `mlth`/`mlph`/`mlah`/`mlih`
  (list headers carrying a **semantic count at +8, not a byte length** —
  advance by `headerLen` only), `mith` (track), `miph` (playlist), `mtph`
  (playlist item), `miah`/`miih` (album/artist items), `mhoh` (string).
- **BE (legacy PowerPC)**: `hdsm`/`htim`/`hpim`/`hptm`/`hohm`. Parse
  supported (`itl_be.go`); **writeback refused** (`ErrBEWritebackUnsupported`,
  K12).
- **The central parsing subtlety** (HIGH-2, the bug that blinded all string
  diagnostics until T001): every chunk has `headerLen` at +4 and `totalLen`
  at +8; for container tags (`mith`,`mhoh`,`miah`,`miph`,`miih`) the outer
  cursor advances by `totalLen` but children live in
  `[offset+headerLen, offset+totalLen)` and must be inner-walked. Two layouts
  exist in the wild: nested (children inside the mith span) and flat-sibling
  (`itl_le.go:123-133`).
- **PID byte order differs**: LE stores track PIDs reversed on disk
  (`parseMithLE` reverses `data[base+135-i]`); BE stores MSB-first. Hex form
  everywhere in code/XML is MSB-first.
- Track field offsets differ per variant (e.g. TrackNumber: LE u16@+44, BE
  u32@+44; DiscNumber: LE u16@+104, BE byte@+104). Annotated layouts:
  `parseMithLE` (`itl_le.go:220`), `parseHtimBE` (`itl_be.go:128-228`).

### A.4 `mhoh` string blocks (the historic corruption epicenter)

40-byte fixed header (`mhohFixedHeaderTotal=40`, legal `headerLen`=**24**,
100% uniform across 965,223 golden blocks):

| offset | field |
|---|---|
| +0 | `"mhoh"` |
| +4 u32 | headerLen (must be 24; damaged libs had 41–210 → K5) |
| +8 u32 | totalLen = 40 + strLen (K7) |
| +12 u32 | hohmType |
| +24 u32 | **encoding indicator (the real one)**: 0=ASCII, 1=Win-1252, 2=UTF-8, 3=UTF-16**LE** |
| +27 byte | legacy flag — iTunes writes **0x00 always**; our old writer stamped 1/3 here → K3/CRIT-1 |
| +28 u32 | strDataLen |
| +32..39 | reserved, zero |
| +40… | string payload |

- hohmType map: 0x02 Name, 0x03 Album, 0x04 Artist, 0x05 Genre, 0x06 Kind,
  0x08 Comment, **0x0B LocalURL** (`file://localhost/…`, percent-escaped),
  0x0C Composer, **0x0D Location** (native Windows path, `W:\…`), 0x64
  playlist Name, 0x65/0x66 smart-criteria **binary blobs** (never decoded),
  ~40 more in `mhoh_encoding_table.go:94-292` (corpus-derived, 2026-06-09).
- Three types (0x15, 0x69, 0x6C) have non-standard length layout — exempt
  from len-arithmetic/tail-zero checks.
- **Two encoding conventions share the same numbers with different meanings**
  (legacy +27: 1=UTF-16**BE**, 3=Win-1252; corpus +24: 1=Win-1252,
  3=UTF-16**LE**). `decodeMhohBlock` (`mhoh_string.go:261-300`) discriminates
  on `+27==0`. **K16 (open defect):** this discriminator misclassifies large
  classes of iTunes-authored blocks in both directions — golden-library
  Locations render as byte-swapped CJK mojibake (ASCII decoded as UTF-16LE)
  and real UTF-16LE renders as NUL-interleaved 8-bit. Consequence:
  `location-form` reports ~2× tracks-with-location violations on every
  known-good library, and any decode→encode round-trip of a misclassified
  string would write real corruption.
- Writer (`encodeMhohITunes`, `mhoh_string.go:99-139`) is corpus-table-driven
  and errors rather than guesses on unknown types; out-of-corpus blocks are
  preserved byte-for-byte with a WARN.
- The 0x0D/0x0B **LocationPair invariant** (SPEC 2 §1b): one canonical
  Windows path, rendered twice (0x0D plain, 0x0B `file://localhost/` +
  percent-escaping); always written as a pair; URL-in-0x0D is
  unrepresentable via `LocationPair` (K4/CRIT-2 fix).

### A.5 Known unknowns (fields treated as opaque)

- hdfm `unknown` @0x0C; entire `headerRemainder` minus 4 counts (+ now the
  Library PID read for K13).
- BE `htim` unknowns at +24/+52/+56/+62/+80/+84/+109/+124/+136; `hptm`/`mtph`
  16 bytes at +8..23 (checked-state flag not parsed — TODO `itl_be.go:70`).
- Smart criteria/info blobs (0x65/0x66) — copied raw.
- Playlist folder detection unsolved (`ITLPlaylist.IsFolder` TODO,
  `itl.go:36`); `Bookmarkable` assumed true (`itl_convert.go:51`).
- msdh blockTypes 4, 12–16, 19–23: inventoried, semantics unknown; copied
  as-is on rewrite.
- `maxCryptSize == 0` → magic 102400 fallback: boundary heuristic, untested
  against iTunes variants that might use another value.

**Foolproofing implication of every opaque field:** we cannot detect desync
in what we cannot read. The mitigation is not to parse everything — it is to
(a) preserve byte-exactly, (b) verify round-trip byte-identity where nothing
was mutated, and (c) anchor to external truth for what the file *means*.

---

## Part B — Empirical census (server audit, 2026-07-03)

`itl-check` (cross-compiled linux/amd64) against every library on
`<server>`:

| Library | Tracks | Playlists | Ver | Verdict | Notes |
|---|---|---|---|---|---|
| `books/itunes/iTunes Library.itl` (live, Jun 25) | 95,238 (93,677 loc) | 353 | 12.13.10.3 | FAIL location-form only | K16 false positive; +4,338 tracks vs golden |
| `.itunes-master/iTunes Library.golden.itl` (RO, Apr 2) | 90,900 (89,339 loc) | 335 | 12.13.10.3 | FAIL location-form only | K16 false positive |
| `.itunes-writeback/iTunes Library.itl` (Jul 1) | **374 (0 loc)** | 14 | 12.13.10.3 | **PASS all 8** | the stub; operator-planted baseline (see SPEC 3 §1) |
| `…writeback/*.bak-2026062x/070x` (×5) | 374 | 14 | | PASS | byte-identical stub copies — rotation had consumed all slots |
| `…(Damaged).itl` | 90,898 | 335 | | FAIL ×4 | hdr 90,900≠90,898 (K2), 2 dangling mtph (K1), 1.48M mhoh (K3/K5), URL-in-0x0D (K4) |
| `…(Damaged) 1.itl` | 90,898 | 335 | | FAIL ×3 | as above minus dangling refs |
| `…(Damaged) 2.itl` | 90,900 | 335 | | FAIL ×2 | itl-repair'd twin of damaged-1; **still iTunes-rejected** |
| `…(Damaged) 3.itl` | 90,863 | 335 | | FAIL ×2 | only 646 bad mhoh + `.itunes-writeback/` staging-path leak |
| `….itl.repaired-bak-20260502-083408` | 90,900 | 335 | | K16-only | the successful May 2 repair output |
| `….itl.rebuild-bak-20260502-075930` | 0 | 14 | | FAIL parse | failed rebuild attempt |
| `….itl.tiny-snapshot-20260502-183252` | 65 | 0 | | FAIL ×4 | failed intermediate |
| `Previous iTunes Libraries/…2022-12-22.itl` | 40,913 | 165 | 12.12.6.1 | K16-only | historical |
| repo `testdata/itunes/iTunes Library.itl` | 85,579 | 313 | 12.13.9.1 | K16-only | CI corpus candidate |

Other findings: `iT 1.tmp` (131 MB) is an **XML export**, not an .itl;
`bkup/itunesgood/` is a full known-good set matching the Apr 2 golden;
`books/itunes/Backup/` is iOS device backups (unrelated). XML `<key>Track
ID</key>` grep-counts are inflated ~4× by playlist membership entries — only
the binary parse is authoritative.

**Stub forensics (K13 exemplar):** same Library PID `48E87F59865568B0` as
golden, **0/374 shared track PIDs**, all-music cloud-only content (no
Locations). Operator-confirmed provenance: iTunes failed to load its library,
rebuilt fresh, pulled the account's cloud music; the operator then copied it
in as a deliberate safe baseline. Passing all 8 guards is the finding.

---

## Part C — The safety pipeline, adversarially

### C.1 Architecture

- Hardened chokepoint `itunes.SafeWriteITL` (`itl_safe_write.go:180`):
  8-step protocol — in-use gate (unwired) → read/parse/refuse-BE → mutate a
  copy → **regenerate header counts** → in-memory contract → `.itl.new` +
  fsync → **re-read from disk, contract again** → `.bak-<RFC3339>` + atomic
  rename + dir fsync → rotate (keep 10, `.bak-lkg` pinned).
- **Second, weaker wrapper** `itunesservice.SafeWriteITL`
  (`writeback_batcher.go:582`): only `ValidateITL` around
  `ApplyITLOperations` → `.tmp` → rename; own `.bak-YYYYMMDD-HHMMSS` keep-5
  scheme. This is what the batcher flush and `server/itl_rebuild.go` call.
  (It reaches the real contract indirectly via `safeEncodeITL`, but skips the
  re-read verification, lkg pinning, and unified rotation.)

### C.2 Guard-by-guard: detects vs blind

| Guard | Detects | Blind to |
|---|---|---|
| parse-roundtrip | non-LE, unlocatable master list, 0 tracks; fail-closed | any magnitude > 0; semantics |
| container-tiling | truncation/splice/gap | valid-but-wrong content |
| count-coherence | internal mlth/mith + header/payload desync (K2), per-miph counts (K8) | **anything external — header is regenerated from the same payload first (self-referential)** |
| no-new-dangling-refs | new orphan mtph (K1); fail-closed | removals that also fix playlists; rebuilt-empty playlists |
| mhoh-format | +27≠0 (K3), headerLen≠24 (K5), len arithmetic (K7), bad +24 | byte-perfect wrong content; missing blocks |
| location-form | URL-in-0x0D (K4), staging-path leak, unpaired 0x0B | **vacuous at 0 locations; permanently noisy via K16** |
| tid-pid-sanity | TID order/dupes (K6/K11), zero/dup PIDs (K9) | count loss |
| expected-magnitude *(new)* | K14 shrink/growth vs external count | disarmed unless caller wires ExpectedTrackCount |
| library-identity *(new)* | K13 library swap / population replacement | disarmed on first run (no sidecar) |
| bounded-delta | >5000 removals, >20% mhoh rewrite | **before==nil (all audit use); Force (rebuild/export always); replacement-shaped shrink** |

### C.3 Swallowed errors / dead code (exact sites)

- `writeback_batcher.go:353-363` — diff-step parse failure: WARN + writes all
  updates anyway.
- `writeback_batcher.go:533-536` — flush `SafeWriteITL` error: WARN + drop
  (no retry/alert).
- `writeback_batcher.go:596-597` — backup failure: WARN, proceeds with no
  rollback anchor. `:216-220` — `MarkExternalIDRemoved` discarded in a
  goroutine. `:543-547` — `MarkITunesSynced` WARN-only. `:370-372,406` —
  per-item DB errors skipped.
- `itl_le_verify.go:80-85` — exported legacy `VerifyITLNoDanglingRefsLE`
  still **fail-open** (nil when master list unlocatable); the contract's G4
  fixed this only inside the contract.
- `PinLastKnownGood` — **zero production callers** (test-only) → `.bak-lkg`
  never exists. `SetLibraryNotInUse`/`WithLibraryNotInUse` — **zero
  production callers** → in-use gate always WARN-and-proceeds.
- `rebuild.go:140-142,176-178` — `GetBookFiles` errors fall through to
  empty/default Location.

### C.4 Rebuild path blast radius

`RebuildITLFromDB` (`rebuild.go:249-305`, `POST /api/v1/itunes/rebuild-full`)
removes ALL tracks and re-adds only DB books with `IsPrimaryVersion && !
MarkedForDeletion && ITunesPersistentID != ""`, under
`ForceContractConfig()`. An under-populated/mid-migration DB → a tiny,
guard-passing library written in place. `BuildExportITL` shares the Force
blindness (export-only). This is the highest-leverage K15 site.

---

## Part D — Failure history (chronological taxonomy)

Sources: `fable5-review-findings.md`, `fable5-spec-itunes-writeback-hardening.md`,
`docs/itunes-sync-diagnostic-suite.md`, git log. Four production "(Damaged)"
libraries are the evidence base (all produced by this tool's writeback).

**Phase 1 — May 2026 (point-patch era):**
- FM-1 (May 2, `23b127ab`): `RemoveTracksByPIDLE` left dangling mtph → K1.
  "Fix" = neuter removal to a no-op + fail-open verifier. Point patch on the
  *minority* class.
- FM-2 (`ec641693`): `cmd/itl-repair` strips orphan mtph. Proof of
  insufficiency: damaged-2 = repaired damaged-1, **still rejected** (K2/K3/K5
  remained).
- FM-3 (`9bc0c19c`): sync diagnostic suite (Apple Devices crash at
  "Determining tracks to sync"); surfaced clues, fixed nothing.
- FM-4 (`fbb3bfc9`): flush circuit-breaker → later generalized into
  bounded-delta.

**Phase 2 — late May:** corruption still live 27 days post-"fix"; PD-2
bisect over 275 commits proposed, **never executed** (resolved by forward-fix
instead — consistent with a *class* of writer defects, not one regression).

**Phase 3 — June 2026 (structural era):**
- FM-8 (Jun 5, `3b5ce221`): K5 headerLen writer fix — but no read-time
  detector (HIGH-6), and the dominant K3 stayed live 4 more days.
- FM-9 (Jun 9): fable5 review names the real classes — CRIT-1 (+27 flag),
  CRIT-2 (URL-in-0x0D), CRIT-3 (stale header counts) — all still LIVE.
- FM-10..16 (Jun 9–10): T001 mhoh-descent parser fix (strings were never
  read!), T002 encoding corpus, T010 fail-closed inflate, T003 the 8-guard
  contract, T004 SafeWriteITL + header regeneration, T005 conformant
  encoders, T006 LocationPair, T007 honest itl-diff/itl-check, T008
  diff-before-write.
- FM-17 (Jun 26): `itunes-heal` — adjacent DB-reference staleness (~19,922
  tracks) from the June organize bug; ongoing mop-up, not .itl damage.

**Recurrence patterns (the meta-lessons):**
1. Point patches on minority classes let the majority class run ~5 more weeks.
2. **Fail-open shipped twice independently** (verifier + inflate) — passing
   exactly when the library was most damaged.
3. Partial fixes ordered by ease, not dominance (K5 before K3).
4. Diagnostic tools produced false confidence during a live incident
   ("0 changed" on 167K-block rewrites) because the parser never read
   strings.
5. **July 2026 continues the pattern one level up:** structure got fixed;
   *identity and magnitude* — semantics — were the next unmodeled class, and
   the stub sailed through. Each era's guards perfectly catch the previous
   era's failures.

---

## Part E — Roadmap (normative list; SPEC 3 holds the implemented detail)

**Tier 1 — external truth anchors (IMPLEMENTED 2026-07-03,
`feat/itl-identity-guards`):** `library-identity` guard (K13, sidecar
fingerprint: Library PID + ≤1024-PID sample, ≥90% overlap, `AdoptLibrary`
bless), `expected-magnitude` guard (K14, ±10% vs external count), sidecar
lifecycle in `SafeWriteITL`, 7 tests incl. end-to-end stub-rejection.

**Tier 2 — close the bypasses (K15):** de-hardcode Force in
rebuild/export + report-mode bounded-delta under Force; symmetric
bounded-delta (|Δ| + replacement ratio); unify both write wrappers on the
hardened path; flush errors → metric+alert; wire `PinLastKnownGood` and
`SetLibraryNotInUse`; batcher parse failure → abort+re-enqueue.

**Tier 3 — fix the decoder, then trust the guards (K16):** repair
`decodeMhohBlock` discrimination (both-directions mojibake on golden);
acceptance = **0 location-form violations on the golden master**; CI-audit
the checked-in testdata golden so a guard failing on known-good data fails
the build.

**Tier 4 — model iTunes as co-writer:** record post-write file checksum;
on next write, mismatch → re-parse + identity re-check + require the foreign
diff to be explainable (play counts/dates) before layering mutations;
`ExpectedTrackCount` wired from the DB census on every batcher flush.

**Standing principles distilled from the whole history:**
- Fail closed everywhere; a check that can't run is a failure, not a pass.
- No self-referential validation — every "coherence" needs an anchor outside
  the bytes being validated.
- Bypasses are per-write operator acts (`AdoptLibrary`), never baked into a
  call site (`ForceContractConfig()` in rebuild).
- A guard that fires on known-good data is worse than no guard (alarm
  fatigue); golden-corpus CI keeps guards honest.
- Preserve opaque bytes exactly; verify byte-identity of everything not
  deliberately mutated.
- iTunes co-authors the file; validate continuity on read, not just
  correctness on write.
