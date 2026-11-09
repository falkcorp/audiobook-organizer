- [ ] **PLATFORM-CORE-R1-25-HNSW-CLEAN-STOP** Cross-seam with the R1-15
      HNSW follow-up (#4506, which superseded #4496; open when R1-25 landed). Whichever of the two
      merges second does all of this in its own PR:
      1. `internal/books/offline_swap.go` `removeHNSWCleanStop`: replace the
         hard-coded `<db parent>/hnsw/hnsw.clean-stop` path with
         `database.TakeHNSWCleanStop(database.HNSWSnapshotDir(dbPath))`,
         keeping the directory sync, so the offline commands (`storage
         restore-offline`, `storage reset`) and the live restore stager
         remove the same marker #4506 writes.
      2. Live restore (`internal/books/restore_stager.go`): the service is
         running when it stages, and its clean stop at exit 75 writes the
         marker again after `BeforeStage` removed it, so the boot after the
         swap trusts a graph of the replaced store. Give `restoreStager` the
         live store's `(*database.HNSWEmbeddingStore).MarkDiverged(reason)`
         (nil when HNSW is off) and call it in `BeforeStage` before the
         marker removal, that is before `restorejournal.CreateIntent` links
         the intent. Extend `TestLiveRestoreStagesAndExits75` with HNSW on:
         no marker after the exit, and the next boot reconciles.
      3. #4506's `PrepareRestoreIn`/`Extract` split and the handler's
         `MarkDiverged` between them change HEAD's in-place
         `RestoreBackupIn`, which R1-25 deleted (the route now stages
         through `backup.StageRestore`). Port its archive refusals that
         `backup.ExtractStore` lacks, `ErrArchiveCarriesHNSWSnapshot` at
         least (`extractArchive` already confines entries and refuses an
         empty archive), into `ExtractStore`, and delete `PrepareRestoreIn`
         if nothing else calls it.
