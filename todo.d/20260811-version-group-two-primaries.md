- [ ] **VG-DOUBLE-PRIMARY** A version group can end up with **two** members both
      flagged `is_primary_version=true`, so a merged book shows twice in the
      library forever. Found 2026-08-11 while investigating the owner's report
      that combined books still list individually.

      **Measured on prod, cache-busted so this is not the stale-list defect:**
      sampled 15 non-primary books at `offset=500`; all 15 sat in genuine
      multi-member groups **with** an elected primary — so there is no
      orphan-group defect. But **10 of those 15 groups had two primaries out of
      three members.** Reproduced on the plain (non-search) list path the UI
      uses:

      ```
      ?limit=20&is_primary_version=true&filters=[{"field":"title","value":"Dungeon Tour Guide"}]&_cb=201
      → count=4
        01KNDBMH1SJ29JF4ZTS5NTETPF  grp=01KNDBMH1SJ29JF4ZTS6S5B1X0  primary=True  created 2026-04-04
        01KZQQVA66GVMFNVWPA9T0V2EE  grp=01KNDBMH1SJ29JF4ZTS6S5B1X0  primary=True  created 2026-08-11
        01KNDC7G0GCX0ZPTJDXQG8NQ3N  grp=01KNDC7G0GCX0ZPTJDXSRQFF2X  primary=True
        01KZR8862HZJE7AWMTGQFADEHG  grp=01KNDC7G0GCX0ZPTJDXSRQFF2X  primary=True
      ```

      Two groups, four rows. The list filter is an exact index lookup on `true`
      (`internal/database/memdb_summaries.go:133`,
      `internal/database/memdb_reads.go:623-628`), so both members list. This is
      **independent of the response-cache staleness bug** — it persists after a
      cache bust and after a restart.

      **Candidate writers — none verified as the causal path for these rows:**

      - `internal/merge/service.go:196-206` reuses an existing group's ID but
        writes `IsPrimaryVersion` only on the books passed **into** the call.
        Pre-existing members of that group are never demoted. This is the
        strongest candidate.
      - `internal/reconcile/reconcile.go:770-795` promotes `kept` and demotes
        only `originals`, not sibling library copies.
      - `internal/reconcile/reconcile.go:1358-1367` mints a new group + primary.

      The newer half of each observed pair is a `01KZ…` ULID minted 2026-08-11
      at an `organize`d path, which *looks like* the organize/library-copy path
      minting a second primary into an existing group. **Not verified** — do not
      start from that assumption without confirming it.

      **Needs both halves:**
      1. Forward fix — enforce one primary per group. When reusing an existing
         group ID, load every current member and demote them.
      2. Backfill — the existing double-primary rows do not self-heal. Needs a
         maintenance repair op. Scope unknown: 10 of 15 sampled is not a
         library-wide rate, it is a sample of one offset window. **Measure the
         real count before sizing the repair.**

      Add an invariant test that a group can never have more than one primary,
      and run it against the existing data as a diagnostic before writing the
      repair.

      Related but distinct: the 24h list-cache staleness
      (branch `fix/list-cache-generation`) masks merges too, but that one is a
      read-path bug that a restart clears. This one is real, persistent data.
