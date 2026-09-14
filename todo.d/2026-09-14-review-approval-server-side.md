- [ ] **Record review-lane approvals server-side.** The batch apply trusts two
      client claims: a pin with `origin: "row"` (which now makes the row
      overwrite filled fields, owner ruling 2026-09-14) and the owner-review
      gate override it unlocks. Any API-key caller can build that pin from the
      review list's own `candidate_hash`, so both are claims, not proof that
      the owner looked at the row. Accepted for now (owner, 2026-09-14).
      Record the approval on the server when the owner clicks Apply in the
      review lane (who, when, which candidate hash), and have
      `planCachedApply` in `internal/server/batch_apply_one.go` check that
      record instead of the request's pin origin.
