### Fixed

- **`TODO.md` claimed a production run that had in fact happened.** Two entries — the
  CONS-10/INIT-2 T6 exact-backlog drain and error-correction T04 — recorded the 2026-07-18 prod
  triage as never executed, while `docs/dedup/STATUS.md` and `docs/operations/pending-prod-actions.md`
  recorded it as done. Settled against the production journal rather than either document:
  the op ran with `apply=true`, `dismissed=7891`, `dismiss_errors=0`, `outcome=completed`.
  `TODO.md` was the stale one and is corrected.
- **A reported "purgeable count drift" was not one.** The 2026-08-11 docs audit flagged 7,878
  vs 7,891 as an unexplained inconsistency. They are the **sandbox** (10,304 candidates) and
  **production** (10,319) measurements of the same operation — both correct. Neither line said
  which population it described; both now do.
