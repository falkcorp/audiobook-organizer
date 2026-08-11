- [ ] **ITUNES-SMARTCRIT-PARSE** `ParseSmartCriteria` does not understand the
      real Smart Criteria format. It reports success on every blob and returns
      **garbage**. Measured 2026-08-10 against all 292 smart playlists in the
      owner's live `iTunes Library.xml`.

      🚨 **This is a "reporting success while meaning nothing" defect**, and it
      is worse than a parse failure would be: `ParseSmartCriteria` is documented
      as "tolerant of unknown fields/operators — those are recorded as raw hex
      rather than causing parse failure," so a totally wrong layout yields
      `err == nil` and a full-looking rule list. **292/292 blobs parsed with
      zero errors and 1,751 rules — and essentially every rule is empty:**

          PLAYLIST "Audiobooks" conj=OR rules=2
             field=unknown_0x2000000 op=op_0x00 operands=[]
             field=unknown_0x00      op=op_0x00 operands=[]

      One operand decoded as `72057594037927936` — that is `0x0100000000000000`,
      a byte-order artifact, confirming the endianness is also wrong.

      **Is the criteria plain text in the XML?** No. `Smart Criteria` is a
      base64-wrapped **binary** blob — byte-for-byte the same `SLst` structure as
      in the `.itl`. The XML's advantage is only that the blob is *present*
      (292/292) where ITL extraction yields 0. **But the string operands inside
      the blob are plain UTF-16BE and are recoverable without a full parser.**

      **What the format actually is** (derived from the 292 real blobs, and the
      two claims marked ✅ are the only ones validated against ground truth):

      - ✅ Magic `SLst` (`0x534c7374`) at offset 0 — the parser never checks it.
      - ✅ **Big-endian**, not little-endian.
      - ⚠️ It is **NOT** a flat `header + rules[]` array. The blob is a **nested
        tree of `SLst` containers** — an 850-byte single-rule blob contains
        *three* `SLst` magics (offsets 0x000, 0x0C0, 0x1FC). An earlier revision
        of this entry claimed a flat 136-byte header with variable-length rules;
        parsing all 292 blobs that way **overruns on 281/292** and that claim was
        wrong. The container nesting is still unmapped.

      **A parser is not required to extract the rules.** Operands can be located
      structurally and the surrounding rule header read at fixed negative
      offsets. Over all 292 blobs this yields **358 operands whose declared
      length matches**, which is what proves the alignment:

          find a UTF-16BE run at offset `off`
          require  u32be(off-4)  == len(run)*2     # length prefix agrees
          field  =  u32be(off-56)
          operator= u32be(off-52)

      **Field codes** (over alignment-proven operands): `3`=Album ×204,
      `4`=Artist ×126, `8`=Genre ×23, plus `71` ×2, `2` ×2, `14` ×1.

      **Operator words — validated against actual playlist membership**, by
      testing whether the materialized members really satisfy the rule:

      | word | meaning | evidence |
      |---|---|---|
      | `0x1000002` | contains | satisfied **4017/4017 = 100%** |
      | `0x3000002` | does **NOT** contain | satisfied **0/23700 = 0.0%** |
      | `0x1000001` | **UNRESOLVED** (22 uses) | 18.2% — fits neither |

      🚨 `0x3000002` is a **perfect inversion**. Treating every `…0002` word as
      "contains" would ship 79 playlists matching exactly the books they were
      written to exclude — silently, and looking like a successful import. Do not
      map an operator word without running this membership check on it.

      **Conjunction:** with negation applied, **38/46** multi-rule playlists have
      every rule holding for >95% of members, so rules are predominantly ANDed;
      the other 8 (e.g. `Recent Litrpg`, 10 rules) are ORs. The AND/OR flag has
      **not** been located — candidate is the u32 at offset 8 of each `SLst`
      container (`0x02` outer vs `0x01` inner in the dump), untested.

      **Why no test caught the original defect:** `playlist_sync_test.go` builds
      `ITLPlaylist` values by hand with `IsSmart: true` and never exercises a
      real blob, and the tolerant-by-design error handling means a round-trip
      over real data still returns `nil`. Any fix must assert on **rule
      content** — a non-empty `Rules` slice is not evidence, since the broken
      parser produces 1,751 of them. The membership check above is the ground
      truth to assert against, and it needs no prod access.

      **Two independent recovery sources, covering 290/292 (99.3%):**

      1. **224 of 292 carry materialized `Playlist Items`** (116,822 track refs)
         — actual current membership, needing **no criteria parsing at all**.
         Imports as a static snapshot.
      2. **66 more have criteria strings but zero materialized items.** For these
         the operands are the only source.
      3. **2 have neither** and are unrecoverable from the XML.

      **The split is convenient:** the 68 zero-membership playlists are almost
      exactly the *series* ones (`Ascend Online`, `Aurora Cycle`,
      `Anime Trope System`, `A Snake's Life`) — the ones the owner expects to
      become obsolete once series support lands. So the criteria parser is the
      **low-value half**: the 224 that can be snapshotted need none of it.

      **Track resolution is by persistent ID, not path.** `Playlist Items` give
      Track IDs, which resolve 100% within the XML to a `Tracks` entry carrying a
      `Persistent ID`; `BookFile.ITunesPersistentID` is indexed with
      `GetBookByITunesPersistentID`. This sidesteps the `itunes_path`
      normalization bug entirely — the XML `Location` values are Windows drive
      paths (`file://localhost/W:/itunes/iTunes Media/...`) and should not be
      used for matching.

      ⚠️ **NOT MEASURED, and it gates option 1:** what fraction of the XML's
      track persistent IDs actually exist in our DB. Measure that before
      promising 224 importable playlists — if PID coverage is poor, both recovery
      paths are moot.

      Do not wire criteria-based import until the operator mapping is asserted
      against real membership — right now it would silently import 292 playlists
      whose rules are all empty.
