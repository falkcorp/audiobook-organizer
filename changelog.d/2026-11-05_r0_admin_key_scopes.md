### Security

- Admin-only routes honour API-key limits. An administrator's API key that was
  limited to some permissions can no longer use the administrator role: the
  admin debug edits, the master lists, account merging and setting another
  user's email now refuse it. A key with every permission, a key with no
  limits, and a signed-in administrator are unchanged.
