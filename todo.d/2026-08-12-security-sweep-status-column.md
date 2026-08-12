## Docs

- [ ] **Give the 2026-06-22 security sweep a status column so it can eventually be retired.**
      `docs/audits/2026-06-22-repo-optimization-security-sweep.md` carries **41 finding IDs**
      (ARCH-1..8, FE-1..8, PERF-1..8, SEC-1..9, TOOL-1..8). Exactly **one** of them (PERF-1)
      appears anywhere in `TODO.md`. There is no other tracker, so the document is the sole home
      of ~40 findings whose current state nobody knows.

      It is demonstrably still live — `changelog.d/20260810_213500_make_test_everything.md:22`
      draws down TOOL-8 — so it cannot be archived. But it also cannot be *trusted*: a
      2026-08-12 spot-check of 5 IDs found 4 already fixed (SEC-1, SEC-3, SEC-4, TOOL-2) and 1
      still live (SEC-9, filed separately). At that rate most of the document is describing
      problems that no longer exist, which makes the few real ones easy to miss.

      The cheap fix is a status column — verify each of the 41 against HEAD once, mark
      fixed/open/obsolete with a `file:line`. Then the open ones can move to `TODO.md` and the
      document becomes archivable. This is a bounded, mechanical pass, and it is the thing
      standing between this audit and retirement.
