- [ ] **E07 residue: 2 ambiguous duplicate-PID groups need a human pick.**
      The 2026-08-14 live census (`GET /api/v1/itunes/pid-integrity`) shows
      the duplicate-PID population is down to 2 groups — the same Alcatraz
      content present in both the organizer tree and the iTunes tree
      (sample PID `31A790B4DEF5981C`). The recorded "8,984 auto-resolvable"
      was stale pre-repair state. `files_to_clear=0`, so the repair op has
      nothing safe to do automatically; an operator must pick the canonical
      file per group. The iTunes-tree copies are HANDS-OFF — resolution must
      clear the organizer-side copy or be deferred, never touch the iTunes
      tree.
