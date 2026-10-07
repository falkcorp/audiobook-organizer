<!-- file: docs/executive-summaries/2026-10-07-api-key-hardening-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8cf09508-ec7f-49a1-8225-1cd5183659d8 -->
<!-- last-edited: 2026-10-07 -->

# API keys expire, and can no longer turn into a signed-in admin

Branch: `fix/apikey-expiry-and-privilege`. Plan and every decision:
`docs/plans/2026-10-07-apikey-expiry-and-privilege.md`.

## Executive Summary

- **An API key can no longer make itself a signed-in admin.** Before this,
  an admin API key could reset any user's password (or create a new admin),
  then sign in as that user and act as if it were you, including on the
  Repairs actions only you are meant to approve. Now no API key can change a
  password, create or invite a user, switch a user on or off, make a sign-in
  link, make a key for someone else, wipe or restore the database (which
  replaces every account), or change who is allowed to sign in. It
  gets a clear "sign in to do this" refusal. Doing these things while signed
  in works exactly as before.
- **Bootstrap keys now last 8 hours, not 30 days.** The emergency admin key
  the server hands out was assumed to expire after a working day. Only the
  local copy was deleted after 8 hours; the key itself kept working for 30
  days. It now stops working after 8 hours, and no setting can stretch it past
  a day. A key it creates can't outlive it.
- **Every API key expires.** A new key lasts 30 days unless you pick longer,
  up to a year. "Never" is gone from the menu.
- **Existing keys keep working for a month.** Keys that never expired get a
  30-day expiry the first time the new version starts. The server log lists
  each one, and the API keys page shows when each key expires (it used to show
  a future date as "-29d ago").

## What you might notice

- Anything that created users or reset passwords with an API key now gets a
  403. Do it from the web app while signed in.
- Integrations whose keys never expired (a metrics scraper, a fingerprint
  worker) stop working 30 days after this deploys unless you rotate or replace
  their keys. The startup log names them.

## What is still open

- A key with the settings permission can still point the server at a
  different helper program or database file. That is a bigger hole than
  this one (it is not about signing in) and was found, not fixed. Your call
  whether those settings become locked to the server's own configuration.

- A signed-in admin session can still reset a password or make a sign-in
  link, by design. Limiting the owner-only Repairs actions to a Cloudflare
  Access login, or adding a second factor at the click, is what fully closes
  that. Your call; it is on the TODO list.
