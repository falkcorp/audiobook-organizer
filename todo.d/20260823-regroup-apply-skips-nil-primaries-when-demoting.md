<!-- file: todo.d/20260823-regroup-apply-skips-nil-primaries-when-demoting.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0066e18a-cc1a-42db-9542-5fb60f984f8e -->
<!-- last-edited: 2026-08-23 -->

- [ ] **`regroup_apply.go` skips nil members when demoting, so it can still leave a
      double-primary group.** Same invariant as VG-DOUBLE-PRIMARY (TASK-042, fixed in
      `internal/merge`), different file, opposite nil handling.

      `internal/plugins/maintenance/regroup_apply.go:319`:

      ```go
      if m.ID == primaryID || m.IsPrimaryVersion == nil || !*m.IsPrimaryVersion {
          continue
      }
      ```

      `m.IsPrimaryVersion == nil` continues — the nil member is left alone. But the
      store reads nil as PRIMARY (`pebble_store.go`:
      `eff := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion`), so it stays
      effectively primary.

      Concrete input: a regroup hold whose reused target group contains a member
      created before the flag was ever written. After apply the group holds
      {new primary = explicit true, stale member = nil} = two effective primaries.

      Notably this file ALREADY implements group-wide demotion for exactly this
      reason, and does it more carefully than merge did — it re-hydrates via
      `GetBookByID` at :324. The gap is only the nil case. So the fix is to make
      the two agree on nil, NOT to extract a shared helper: the two paths elect
      by different rules on purpose (lowest-ULID here, `BookIsBetter` in merge),
      and merging them would install the second-disagreeing-election bug that
      TASK-042 exists to remove.

      Audited at the same time and found CLEAN, recorded so nobody re-checks them:
      `internal/reconcile/reconcile.go:820,829` (`CleanupDuplicateVersionGroups`
      partitions the whole group and accounts for every member) and
      `internal/reconcile/reconcile.go:1406` (`AssignOrphanVGs` mints a fresh
      single-member group, so there are no pre-existing members to miss).
