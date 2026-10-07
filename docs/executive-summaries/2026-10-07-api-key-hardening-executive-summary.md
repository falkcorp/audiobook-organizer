<!-- file: docs/executive-summaries/2026-10-07-api-key-hardening-executive-summary.md -->
<!-- version: 1.3.0 -->
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

- **Only your Cloudflare Access login counts as you for owner-only actions.**
  The Repairs "Apply (owner)" button (including rows whose files have iTunes
  copies) now works only when you reach the site through Cloudflare Access
  and that login's email is the owner email the server is configured with.
  A password, Google/GitHub, temp-login or invite sign-in no longer counts,
  because the server can create or reset all of those itself; it cannot
  create your Cloudflare login. The page says why when the button is off.
- **API keys also can't change where the server reads, writes or runs
  things.** Changing the library folder, backup folder, database location,
  iTunes file paths, helper programs (fpcalc, Ollama) or plugin settings now
  needs a signed-in session, as does installing a tool, applying an update or
  adding a scan folder. Saving the settings page back unchanged still works.
- **A request carrying both an API key and a Cloudflare login is treated as
  the API key**, with every key limit.
- **A security review of the first version found three ways around it, now
  closed**: spelling a setting with different capital letters slipped past
  the sign-in check; five related pages had no guard; and a key with no
  expiry date was treated as never expiring.

- **A second review found two more gaps in the owner check, now closed.**
  The email match treated some look-alike characters as the same letter (a
  "K" written with the Kelvin symbol matched a plain "k"), so a different
  Cloudflare account with a look-alike address could pass as you. It now
  matches plain letters only. And an approval you gave by clicking could, in
  principle, be picked up by someone else's job; it now only works in the job
  your click started, and stops working if the owner email changes.
- **Changes that can remove or move tracks in your iTunes library are now
  yours alone.** Releasing held track removals, rebuilding the iTunes
  library, moving track locations, re-blessing the library after a swap, and
  uploading or restoring the library file all need your Cloudflare Access
  login. Previews (dry runs) still work for any admin. Every one of these
  actions is logged with who did it.

- **Nobody but you can change who the owner is.** A third review found that
  any signed-in admin (or anyone holding a stolen password) could change the
  owner email to their own, or point the Cloudflare settings at a login
  service they control, and then act as you. Now, once the owner email is
  set, only your own Cloudflare login can change it or those Cloudflare
  settings, and only you can wipe or restore the database (both could clear
  the owner email). The very first owner email can only be set by that
  person, signed in through Cloudflare as that email. Your own API keys can
  never act as you either.

## What you might notice

- Anything that created users or reset passwords with an API key now gets a
  403. Do it from the web app while signed in.
- **Set the owner email** (environment variable `OWNER_EMAIL`, or the
  `owner_email` setting while signed in through Cloudflare Access as that
  same email). Until it is set, owner apply is off for everyone.
- The iTunes settings buttons for write-back and library upload/restore
  now refuse a password sign-in with a message saying why. Use the site
  through Cloudflare Access for those.
- Integrations whose keys never expired (a metrics scraper, a fingerprint
  worker) stop working 30 days after this deploys unless you rotate or replace
  their keys. The startup log names them.

## What is still open

- A key with the settings permission can still point outbound services (the
  OpenAI address, the download client) at another host, which would send that
  host the stored password or key. Your call whether those need a signed-in
  session too; it is on the TODO list with a few one-off actions that take a
  file path.
