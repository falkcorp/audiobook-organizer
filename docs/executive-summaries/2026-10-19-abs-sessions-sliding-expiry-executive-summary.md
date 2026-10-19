<!-- file: docs/executive-summaries/2026-10-19-abs-sessions-sliding-expiry-executive-summary.md -->
<!-- version: 1.0.1 -->
<!-- guid: 50c43bce-f6d8-4831-b9d1-95ea32eda66d -->
<!-- last-edited: 2026-10-19 -->

# Phone apps stay signed in while you use them

PR #4081 (branch `fix/abs-session-sliding-expiry`), not yet merged.
Related spec: `docs/specs/2026-07-29-abs-sync-api-design.md`, section 3.2.

## Executive Summary

- **Phone apps were signed out every 30 days, even in daily use.** The
  audiobook apps that talk to this server (AudioBooth and Absorb) were logged
  out 30 days after sign-in, however often they were used. Now a sign-in lasts
  as long as the app is used at least once every 60 days.
- **A sign-in now stays alive as long as it is used.** Each time the app
  renews its pass, or uses the server (checked at most once a day), the
  60-day clock starts again.
- **The app's short pass now always expires before the sign-in does.** When
  the app's 30-day pass runs out, the app asks for a new one. Before, the
  sign-in itself ended at that same moment, so the request was refused and the
  user was thrown back to the login screen. The server now makes sure the pass
  always expires first, even if an operator sets the two lengths the wrong way
  round.
- **An optional hard limit is available.** Anyone who wants people to sign in
  again after a fixed time, however active they are, can set one. It is off by
  default.
- **Logging out is final, and cannot be undone by a race.** If a logout and an
  app renewal happened at the same moment, the renewal could quietly bring
  the logged-out sign-in back. The server now handles every change to a
  sign-in one at a time and never revives one that was ended.
- **Logs now say what really happened.** An expired pass used to be logged as
  an "invalid" one, which made a routine 30-day expiry look like a changed
  server secret. The two are now logged separately.

## Phone apps were signed out every 30 days

**What it was.** When an app signs in it gets two things: a short pass it
shows on every request, and a sign-in record on the server that lets it get
a new pass. The record's end date was set once, at sign-in, and never moved.
Both lasted exactly 30 days.

**Why it mattered.** On day 30 the pass and the record expired together. The
app asked for a new pass, but the record that would have issued it had just
expired, so the server refused and the app showed the login screen, often in
the middle of listening. Every phone was hit, every 30 days.

**The fix.** The record now lasts 60 days from its last use, not from sign-in.
Each renewal resets the clock, and so does ordinary use, recorded at most once
a day so the server is not writing on every request. Sign-ins that already
existed had the old 30-day end date. On start-up the server moves each one
that is still active to 60 days after its sign-in, so a phone that sits unused
past its old day-30 mark is not caught out either. It never shortens a
sign-in and never revives one that has ended. Phones whose sign-in had already
expired before the update have to sign in once more.

## The pass must expire before the sign-in

**What it was.** The two lengths are settings. Nothing stopped them being set
equal, which was in fact the default.

**Why it mattered.** With equal lengths the app can never renew in time, which
is exactly the logout described above.

**The fix.** At start-up the server checks that the pass is shorter than the
sign-in. If it is not, the server shortens the pass and logs a warning, rather
than refusing to start.

## A logout could be undone by bad timing

**What it was.** Several parts of the server can change the same sign-in
record: renewing it, extending it on use, logging it out, and the regular
clean-up of expired ones. Each read the record, changed it, and wrote the
whole thing back.

**Why it mattered.** If two of these ran at the same moment, the later write
could erase the earlier one. A renewal could quietly reverse a logout, or "log
out everywhere", and an extension could bring back a record the clean-up had
just deleted, in a form the logout function could no longer find.

**The fix.** These changes now happen one at a time. Each one re-reads the
record first, and a sign-in that has been logged out or deleted stays that
way. The clean-up re-checks each record just before deleting it.

## Clearer logs

**What it was.** An expired pass and a forged or corrupted one were both
logged as "invalid".

**Why it mattered.** The routine 30-day expiry looked like a security change
and sent the investigation down the wrong path.

**The fix.** Expired passes are now logged as expired. Expired sign-in records
are also kept for 7 more days before clean-up, so a returning app is logged as
"sign-in expired" rather than as an unknown credential. They never work again,
and they are hidden from the device list.
