- [ ] **`library.import`, `library.organize` and `library.transcode` still carry the
      4h ceiling and `ResumeDrop`.** Only `library.scan` was changed, deliberately —
      it is the one measured to exceed 4h. Check whether the others can also exceed
      their ceiling on a 63k-book library before assuming they are fine; `organize`
      in particular touches every book.

- [ ] **Convert the remaining long-running `ResumeDrop` ops to real resume.** The
      mechanism now exists: `registry.RunItems` gained `ResumeFrom`,
      `CheckpointEvery` and `CheckpointStateFn` (concurrent-safe via a
      contiguous-completion watermark), and 51 call sites route through it. As of
      2026-08-17 the live registry reports 140 defs: 100 `drop`, 19 `restart`, 19
      `requeue`, 2 `ask`. Work through the `drop` list and convert the ones that are
      both long-running and idempotent per item — `metadata.batch-apply-cached`,
      `reconcile.apply` and the full-library sweeps first. Ops that are short-lived
      or unsafe to re-enter should STAY `drop` and get a comment saying why; an
      honest drop is better than a resume that does not work.
