- [ ] **LSH-ACOUSTID-UNWIRED** The head-print acoustic dedup signals never run
      in unified scoring: `dedup.Engine.SetLSHStore` and
      `SetAcoustIDBookFileStore` (`internal/dedup/engine.go`) have no production
      caller, so `de.lshAcoustIDStore` and `de.acoustidBookFileStore` are always
      nil and `lsh_acoustid` / `exact_acoustid` are never collected. Separately,
      `CollectLSHAcoustID` resolves each candidate with
      `GetBookFileByID("", candFileID)`, which builds the key
      `book_file::<id>` and always returns nil (its comment claims a scan
      fallback that does not exist); `GetBookFileByFileID` is the by-ID read.
      Wiring them changes every pair's score, so decide with a corpus rescore.
      Found building 02-PR8 (the window signal is wired through
      `database.AsCapability` in `registry_wire.go`); not fixed there.
- [ ] **WINDOW-ACOUSTID-UI-LABEL** The review UI has no label or colour for the
      new `window_acoustid` signal kind (`web/src/components/review/evidence/signalLabels.ts`,
      `web/src/theme.ts`); chips fall back to the raw kind name. Add both
      alongside `lsh_acoustid`. Found in 02-PR8.
