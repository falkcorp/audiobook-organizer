- [ ] **05-PR7-DEADCODE** 05-PR5 (#4008) added its v3 runner, chunk ledger,
      adapter and Writer-host identifiers to `scripts/deadcode-baseline.txt`
      in a one-time addition the owner approved on 2026-10-13, because
      nothing registers a v3 definition in production until 05-PR7 wires the
      catalog. The added names are every `internal/operations/registry.` line in
      the baseline except the three that predate 05-PR5
      (`ShutdownAllForStore`, `WithRequires`, `dbReporter.droppedLogCount`);
      the block's "05-PR5 one-time addition" comment does not survive a
      `--prune`, which rewrites the file sorted and without comments. When
      05-PR7 lands, run `make lint-deadcode-ratchet` and
      `python3 scripts/deadcode-ratchet.py --prune`, then confirm no
      `internal/operations/registry.` line remains other than those three.
      Any that remain unreachable are dead code to delete, not to keep.
      Also 05-PR7: `make oplint` passes `./internal/plugins/...`, which oplint
      cannot read (it takes directories); run on the directory it reports 586
      stale import violations, and no CI workflow runs it. 05-PR5 only added
      rule R21 (a `Deps` field typed `database.Store`).
