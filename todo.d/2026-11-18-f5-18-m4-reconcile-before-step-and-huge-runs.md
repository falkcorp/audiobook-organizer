- [ ] **F5-18-M4-RECONCILE** Run HEAD's `ReconcileOpsV3WithV2` before the runner
      materialises M4's source range, not inside M4's first `Step` call
      (`internal/books/cutover/m4/step.go`, `reconcile`). The mutex makes no row
      decode before the repair finishes, but the runner's iterator and shard
      boundaries are fixed before that, so a run record the repair creates from a
      v2 row after the snapshot is not visited and `Verify` then reports its
      `opv3:` key as still present (the boot stops loudly, never silently).
      HEAD's own boot already runs the repair every start, so this only bites a
      v2 write made after HEAD's last boot. F5-18 composes the boot: call the
      repair in the window between `Prepare` and `Migrate` (the rig binds
      `Config.Legacy` there) and keep `Config.Reconcile` as the idempotent
      safety net. There is no pre-step hook on `datamigrate.DataMigration` to do
      it inside M4.
- [ ] **F5-18-M4-HUGE-RUNS** Split an imported run larger than one Pebble batch
      across several batches. M4 imports a run, and deletes every row it owned,
      in one batch (its crash-safety invariant is that the run record is absent
      only once all of its rows are). A run holds up to 10,000 item outcomes
      (`opv3ItemCap`) and an unbounded log, so a pathological run is one very
      large batch. `Importer.RunData` already accepts a run's data over several
      calls; the work is a resumable multi-batch protocol for the driver and its
      deletes (write the data batches first, then the record, then the deletes),
      which changes M4's resume story, so it is not folded into this PR.
