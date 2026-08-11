- [ ] 🔴 **The binary ITL parser extracts ZERO smart playlists from real iTunes
      libraries, while the XML export of the same library has 292.** Measured
      2026-08-10 against the owner's live library. This is the blocker standing
      between `maintenance.itunes-playlist-import` and the owner's request
      *"I want all my dynamic playlists from iTunes imported."*

      | Source | Playlists | Smart |
      |---|---:|---:|
      | `/mnt/bigdata/books/itunes/iTunes Library.itl` (live, Jul 19, 32 MB) | 357 | **0** |
      | `.../bkup/itunesgood/iTunes Library.itl` (Apr 2, 28 MB) | 335 | **0** |
      | `/mnt/bigdata/books/itunes/iTunes Library.xml` (same library) | 351 | **292** |

      **Not writeback data loss.** Both the live ITL and an April backup return
      zero, so this is not our ITL writer stripping records — it is extraction
      that never fires on real files. Both parse cleanly otherwise (97,999 and
      90,900 tracks, correct titles, `ver=12.13.10.3`).

      **Not an unimplemented stub either** — that was the first hypothesis and it
      was wrong. `itl_be.go:341-354` and `itl_le.go:429-441` do populate
      `IsSmart`, `SmartCriteria` (hohm `0x65`) and `SmartInfo` (hohm `0x66`).
      The code exists and presumably works on the synthetic fixtures the unit
      tests build by hand — `playlist_sync_test.go` constructs `ITLPlaylist`
      values with `IsSmart: true` directly, so **no test has ever exercised the
      parser that fills those fields.** A third instance of the session's theme:
      the tests pass because they bypass the thing that is broken.

      The XML proves the data exists and is recoverable: 292 `Smart Criteria`
      blobs, with names that are clearly the owner's real playlists (series and
      author names — "A Mage's Cultivation", "Aether's Revival", "Aaron Oster",
      "All the Skills").

      **Two candidate directions — needs a decision:**
      1. **Fix ITL extraction.** Find why the `0x65`/`0x66` branch does not fire
         on 12.13.10.3 files (offset/section assumption, playlist record layout,
         BE-vs-LE path). Highest value: the ITL is the write/authority surface,
         so push-back will need this too. Start by dumping the hohm types
         actually encountered while parsing one real playlist record — the
         parser reaches these playlists (titles are correct), so the records are
         either absent from the stream or skipped.
      2. **Import from the XML export instead.** The criteria are present and
         base64-encoded; `ParseSmartCriteria`/`TranslateSmartCriteria` already
         consume that blob shape. Much cheaper, unblocks the owner immediately,
         and read-only against a file we already parse elsewhere. Downside: the
         XML is an export, not the authority, so it can lag the ITL.

      Recommend **2 to unblock, then 1** — but confirm the XML's base64 blob is
      byte-identical in shape to what `ParseSmartCriteria` expects before
      assuming it drops in.

      `maintenance.itunes-playlist-import` already guards this case explicitly:
      when it parses playlists but extracts zero smart ones it logs a warning
      naming the XML discrepancy and the `grep -c 'Smart Criteria'` check,
      rather than reporting a clean "0 imported" that reads like an empty
      library.
