<!-- file: todo.d/20260806_150100_frontend_open_redirect_invariants.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e34a7b1-05df-4c82-a6e9-3b7150d2f8ce -->
<!-- last-edited: 2026-08-06 -->

- [ ] **Two frontend navigation sinks are unvalidated and safe only by
  accident.** Found 2026-08-06 while auditing the react-router open-redirect
  advisories. Neither is exploitable today. Both rest on an invariant that a
  future change breaks **silently** — nothing fails, nothing warns, the sink just
  becomes live.

  1. `web/src/pages/Login.tsx:78-81` — `location.state?.from` is passed straight
     to `navigate()` with no validation. Safe **only because nothing writes
     `state.from`** (zero writers in the codebase). Wire a `?returnTo=` param
     into it and it is immediately exploitable.
  2. `web/src/pages/BookDetail.tsx:938,968` — `sessionStorage`'s
     `library_return_url` goes to `navigate()` unvalidated. Safe **only because
     the writer runs on the exact routes** `/library` and `/fingerprints`.
     Changing that to `/library/*` makes it exploitable.

  The remedy is to validate at the sink rather than rely on the writer's reach:
  the Go side already does exactly this, and does it well —
  `sanitizeReturn` (`internal/server/handlers/oauth_login.go:260-271`) implements
  the backslash guard the advisory describes, and `abs/openid.go:246-257`
  validates `redirect_uri` before error redirects too. Mirror that on the client.

  🔴 **Do not "fix" [[TODO-SSO-EDGE]] / the OAuth-callback entry at `TODO.md`
  around line 1040 by loosening `sanitizeReturn`.** That entry is a *functional*
  gap — the guard correctly rejecting a custom-scheme return — not a
  vulnerability. Loosening it would convert a working defence into one of these.
