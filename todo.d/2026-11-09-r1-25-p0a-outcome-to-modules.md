- [ ] **Core: tell modules what P0a did, and drop books' swap-slot read** (R1-25 follow-up).
      - **Why.** `books.Options` reads the swap slot before `app.New` (`swapVoidReason`,
        `internal/books/options.go`) to decide whether this boot's pre-boot swap may replace the
        store, and voids the HNSW clean-stop claim `Register` takes when it may. It reads before
        P0a because P0a empties the slot whatever it does, and `module.Deps` carries nothing of
        what P0a did. So it voids on a staged swap that P0a then rejects (one reconcile too
        many), and a swap staged after the read is covered only by that stager's own marker
        discard.
      - **Fix.** A `module.Deps` field with the P0a outcome (`restoreswap.Outcome`: applied,
        swapped back, rejected, nothing staged), which books' `Register` reads in place of the
        slot. Cost: one core module API field, the kernel wiring from `runP0a` to `Register`, a
        core test, the seams notes (`seams.d/R1-15.md` P0a row, `seams.d/R1-25.md`); then delete
        `swapVoidReason` and `TestSwapVoidReason`, and point `TestSwapBootVoidsTheCleanStopClaim`
        at the new field.
