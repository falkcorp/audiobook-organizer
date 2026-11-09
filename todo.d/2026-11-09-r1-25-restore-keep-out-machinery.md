- [ ] **Owner call: delete the restore extractor's snapshot keep-out machinery, now unused** (R1-25
      follow-up; not done in R1-25 so a nine-round-reviewed mechanism is not removed unasked).
      - **Why.** Since R1-25 every restore stages: `backup.ExtractStore` extracts into a new,
        private directory in the swap's staging area and passes `extractArchive` no snapshot
        directories (`snapshotDirs` nil). The keep-out code that compares each entry's physical
        path with the HNSW snapshot directories by identity (`keepOutDir`, `statKeepOut`,
        `statKeepOuts`, `holds`, `physicalPath`, `landsIn`, in `internal/backup/backup.go`) still
        runs a walk per entry but can refuse nothing: no snapshot directory is ever passed, and no
        symlink can exist in the private directory (tar link entries are skipped). The in-place
        restore it protected (`PrepareRestoreIn`, `Extract`, `CheckPathsApart`,
        `CheckSnapshotDirs`) was deleted in R1-25.
      - **Cost of deleting it.** About 230 lines in `backup.go` and the `snapshotDirs` parameter;
        the tests that drive it with snapshot directories (`TestExtractArchive_KeepsEntriesOutOfTheSnapshotDirs`,
        `TestExtractArchive_ReadsTheSnapshotDirsAtEachEntry`, `TestStatKeepOuts`, most of
        `restore_physical_path_test.go`, about 500 lines); `ErrUnresolvableSymlink` if nothing else
        returns it; the R1-15 seams note's Restore paragraph. Keep `entryRefusal`, the scan,
        `checkWithin`, `writeEntry` and the directory syncs, which ExtractStore relies on.
      - **Cost of keeping it.** A per-entry walk on every restore, and a parameter every caller
        passes nil.
