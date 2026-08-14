- [ ] **Metadata matcher: "skip all" / hide-multiples control.** Owner request
      (2026-08-14): multi-match groups need a way to be hidden in bulk —
      a "skip all" that stashes them for a later pass — so a triage session
      can clear the unambiguous rows without wading through the multiples
      every time. Persist the skip set (per user or per session) so hidden
      groups come back on demand, not on reload.

- [ ] **Metadata matcher: apply falsely reports "signed out — no changes were
      made" after a long write.** Owner observed (2026-08-14): with write-to-
      files enabled, a multi-file apply blocks past the auth/session timeout;
      the UI then reports a sign-out AND claims no changes were made — but
      the writes had clearly happened. Two defects: (1) the result message is
      dishonest — never claim "no changes" from a timeout, report "connection
      lost, operation may still be running" and re-query; (2) the root fix is
      the background-job dispatch already filed in
      `20260814-matcher-writeback-background-job.md` — an op id returned
      immediately makes the timeout impossible and the ops screen owns
      progress/results.
