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

      **What the format actually is** (derived from the 292 real blobs):

      - Magic `SLst` (`0x534c7374`) at offset 0 — the parser never checks it.
      - **Big-endian**, not little-endian.
      - Header is **136 bytes**, not the 8 the parser assumes; a rule count sits
        at offset 8 as a BE uint32.
      - Rules are **variable-length**, not the fixed 136 bytes assumed. Fitting
        `len == 136 + n*124` over all blobs matches only **10 of 292** — the
        long ones carry embedded text operands.

      So the parser has the header size and the rule size roughly swapped, reads
      the wrong endianness, and assumes fixed-width records in a variable-width
      format.

      **Why no test caught it:** `playlist_sync_test.go` builds `ITLPlaylist`
      values by hand with `IsSmart: true` and never exercises a real blob, and
      the tolerant-by-design error handling means a round-trip over real data
      still returns `nil`. Any fix must assert on **rule content** — a
      non-empty `Rules` slice is not evidence, since the broken parser produces
      1,751 of them.

      **Unblocking path, in order:**

      1. **The XML export works as a source.** All 292 blobs extract cleanly from
         `Playlists[].Smart Criteria`, so the ITL extraction gap
         (`ITUNES-ITL-SMART`, 0 of 292) does not block reading criteria.
      2. **224 of the 292 playlists carry materialized `Playlist Items`.**
         Importing those as membership needs no criteria parsing at all and
         would satisfy "import my playlists" today, at the cost of them being
         static snapshots rather than dynamic.
      3. Fixing the parser is what makes them genuinely *dynamic*, and is the
         only path that keeps them updating.

      Do not wire criteria-based import until the parser is fixed and asserted
      on real rule content — right now it would silently import 292 playlists
      whose rules are all empty.
