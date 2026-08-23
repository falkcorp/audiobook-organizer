- [ ] **TODO-051-UNDOC** `docs/api/openapi.json` is missing correctly-prefixed
      entries for 11 live routes that TASK-051 found undocumented while
      deleting group-relative duplicate paths (PR for TODO L296): `/users/invite`,
      `/users/invites`, `/users/invites/{token}`, `/auth/accept-invite`,
      `/deluge/status`, `/deluge/test-connection`, `/itunes/rebuild`,
      `/itunes/write-back-all`, `/users/{id}/deactivate`,
      `/users/{id}/reactivate`, `/users/{id}/reset-password`. Each has a bogus
      group-relative stub at the wrong (bare) path today — do not delete those
      stubs until a correctly-prefixed replacement is written, per
      `.claude/skills/api-doc/SKILL.md`.
