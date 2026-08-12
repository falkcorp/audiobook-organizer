## Dedup

- [ ] **Exact-candidate backlog is re-accumulating — fix the source, not the symptom.** The
      2026-07-18 prod triage drain worked exactly as designed (verified from the prod journal:
      `apply=true dismissed=7891 dismiss_errors=0`), taking exact-pending **9,074 → 1,311**.
      Measured again on **2026-08-12: exact-pending is 5,947** — a ~4.5× regrowth in 3.5 weeks.
      Dismissed also fell 9,242 → 8,258, so candidates are moving between states, not just
      being added.

      A second drain would buy another few weeks and teach us nothing. The question is what
      keeps *emitting* these candidates: the original population was 7,891 title-leak/stub junk
      caused by two iTunes-importer bugs (see `docs/dedup/STATUS.md` and the duration-ms /
      title-leak root-cause notes). Either those bugs still produce leaky titles, or the
      exact-layer keying still treats a stub as a real match.

      First step is measurement, not code: classify the current 5,947 with
      `maintenance.dedup-exact-triage` **in dry-run** and compare the population mix against
      the 2026-07-18 report (purgeable 7,891 / keep 278 / review 2,150). If the mix looks the
      same, the source bug is live; if it has shifted toward `review`, this is normal library
      growth and the alarm is false.

      Also note `stale-drain=3,059` and `stale-fp=384` now appear as exact-layer statuses that
      did not exist in the 2026-07-18 accounting — worth understanding before drawing
      conclusions from the pending count alone.
