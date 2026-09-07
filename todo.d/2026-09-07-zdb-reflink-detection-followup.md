## Real reflink detection via zdb DVA comparison (LOW PRIORITY, 2026-09-07)

The apply-path collision resolver ships with a **proxy** for "is our library copy
already a reflink of this outside file": a size match plus a re-stat interlock,
reusing the precedent from `recover-missing-files` Branch B
(`internal/plugins/maintenance/recover_missing_files.go:829-830`). That proxy is
deliberately weaker than true extent identity and is documented as such in the
code.

A real detection method **does exist** on OpenZFS and was ruled in after the fact:

```
stat -c %i /pool/dataset/fileA      # object/inode numbers
zdb -ddddd pool/dataset <inode>     # dump block pointers
```

Inspect the `DVA[0]=<vdev:offset:asize>` lines. **If two files return identical
`vdev:offset` tuples for an extent, they are physically sharing the same cloned
blocks.** `libzpool`/`libzfs` bindings can compare object block pointers directly
instead of shelling out.

Confirmed available on this deployment: `/mnt/bigdata/books` is ZFS on pool
`bigdata` with `feature@block_cloning = active`, OpenZFS **2.4.1**.

- [ ] **Upgrade the reflink predicate from the size+re-stat proxy to real DVA
  comparison** — ideally as an opt-in verification used only on the branch that
  QUARANTINES a file, leaving the cheap proxy for the common path.

**Costs that made this low priority rather than the default — weigh before doing it:**

- `zdb` normally requires **root**. The service runs as the `audiobook` user, the
  same permission wall that blocked wiping `activity.sqlite` on 2026-09-07. Needs
  a privileged helper or a sudoers entry.
- It is a **subprocess per file** reading pool metadata. Inside the apply path
  behind `writeBackFileGate` at library scale, that is exactly the hotspot shape
  CLAUDE.md's concurrency mandate names — it must be bounded, and skipped whenever
  a cheaper rung already decided.
- `zdb` is a **debugger, not a query API**. On a live imported pool it can read
  inconsistent state or report stale block pointers for an actively-written
  dataset.
- The `libzfs`/`libzpool` binding route is cleaner but means **cgo**, which
  reverses the deliberate no-cgo constraint chosen for the `modernc.org/sqlite`
  driver. Do not adopt cgo for this without deciding that explicitly.
