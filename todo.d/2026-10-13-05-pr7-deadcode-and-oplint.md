- [ ] **05-PR7-DEADCODE** 05-PR5 (#4008) added its v3 runner, chunk ledger,
      adapter and Writer-host identifiers to `scripts/deadcode-baseline.txt`
      in a one-time addition the owner approved on 2026-10-13, because
      nothing registers a v3 definition in production until 05-PR7 wires the
      catalog. When 05-PR7 lands, run `make lint-deadcode-ratchet` and
      confirm every identifier in the block marked "05-PR5 one-time addition"
      has dropped out (the ratchet prints a notice for each one that
      disappeared); prune them with `python3 scripts/deadcode-ratchet.py
      --prune`. Any that remain unreachable are dead code to delete, not to
      keep.
      Also 05-PR7: `make oplint` passes `./internal/plugins/...`, which oplint
      cannot read (it takes directories); run on the directory it reports 586
      stale import violations, and no CI workflow runs it. 05-PR5 only added
      rule R21 (a `Deps` field typed `database.Store`).
