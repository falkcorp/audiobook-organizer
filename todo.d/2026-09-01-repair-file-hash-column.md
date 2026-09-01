- [ ] **TODO-FILEHASH-REPAIR** Repair `book_files.file_hash` rows written by the three
      pre-fix writers. `fix/file-hash-column-algorithm` unified the writers on
      `filehash.BookFileHash` but deliberately shipped no repair. A stored full-file
      SHA-256 and a stored chunked digest are both 64 hex chars and indistinguishable by
      inspection, so repair must recompute. Three populations, three costs:
      (a) whole-file writers (`plugins/maintenance/extract_wav_clips.go`,
      `versions/ingest.go`) — wrong only above the 100 MB threshold, requires a full
      recompute per candidate row to identify;
      (b) the iTunes segment writer (`itunes/service/importer.go`, multi-track groups
      only) — wrong at every size above 1 MB, but **cheaply** detectable: hash the first
      1 MB and compare to the stored value, a match identifies a corrupted row without
      reading the whole file;
      (c) rows never touched by any of the three — correct, leave alone.
      Size the population first with a read-only counting pass before writing anything.
