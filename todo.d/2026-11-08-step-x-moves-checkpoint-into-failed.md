- [ ] **PLATFORM-CORE-R1-24-STEP-X-FAILED-DIR** 05-backup's step x is to
      move a swapped-back checkpoint into `restore/<id>/failed/` (or mark
      it `restore_left`) and write its `delete_after` from the swap-back
      (05-storage §5S.3.12 "Retention", the swapped-back row). R1-21's step x
      does not move the checkpoint yet. R1-24's sweeper already scans every
      spelling of that directory (`restore/<id>/failed/` and
      `restore/<id>/failed/.mediacore-checkpoints/` under the state
      directory, and the swap's own `FailedDest` paths beside the store) and
      starts the 7-day clock at the first sweep that finds a record there
      without a `delete_after`. When step x lands, have it write the
      `delete_after` itself and add a crash-matrix fixture for a failed
      KindCheckpoint restore whose set-aside checkpoint the sweep deletes.
      Found by R1-24.
