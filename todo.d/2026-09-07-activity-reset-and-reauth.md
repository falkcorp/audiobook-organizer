## Activity-log reset feature + reauth gate (2026-09-07)

Follow-ups agreed while fixing the activity SQLite disk blow-up.

- [ ] **Reset Activity Log — admin feature (PR in progress).** Let a user reset
  the activity log from the UI. Requirements: (1) **multi-step confirmation**
  (prompt more than once before doing it); (2) runs as the `audiobook` service
  user so it can wipe its own store without host `sudo` (jdfalk has no write on
  `/var/lib/audiobook-organizer` and no NOPASSWD `rm`); (3) **audit the wipe
  durably so the record survives it** — who cleared it, when, source IP, and
  surrounding request context, written to slog/journald AND seeded as the first
  entry of the fresh log, in case a wipe was malicious; (4) **mask sensitive
  fields (IP, etc.) in the UI by default**, but allow pulling the full unredacted
  audit file/export.
- [ ] **Reauth / passkey reverify gate (future).** Require a step-up reauth
  (passkey reverify or other 2FA) before (a) pulling the **unredacted** activity
  export and (b) any destructive reset. Not built yet; the reset feature ships
  with masking + audit first, this hardens it.
- [ ] **Full-application-database reset (future, GATED).** A "reset everything"
  (books, metadata, authors, versions — the whole Pebble store) reset. **ONLY
  valid after the passkey/2FA reverify gate above exists** — do not build the
  full-DB wipe without step-up reauth guarding it. Blast radius is the entire
  library, so it needs stronger protection than the activity-only reset.
