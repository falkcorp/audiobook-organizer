<!-- file: docs/executive-summaries/2026-09-09-the-credentials-that-moved-house-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7a3f5c81-4e29-4b06-95d7-1c8e04b6f293 -->
<!-- last-edited: 2026-09-09 -->

# The credentials that moved house

**Pull requests:** [#3171](https://github.com/falkcorp/audiobook-organizer/pull/3171)

**Follows on from:**
[The server that forgot where it lived](2026-09-09-the-server-that-forgot-where-it-lived-executive-summary.md)

## Executive Summary

- Moving the app's database to a faster drive also moved three things nobody meant to
  move: the **password that protects all the other saved passwords**, the **emergency
  access token** used to get back into the server when normal logins fail, and a
  read-only access key.
- None of those have anything to do with which drive the database sits on. They ended
  up there because the app worked out where to keep them by asking "where is the
  database?" — in three separate places in the code, each asking the question again for
  itself.
- The visible symptom was that **the emergency way back into the server stopped
  working.** The documented recovery steps read the token from a fixed location. The
  token was no longer written there, and the folder it had moved to was locked down so
  tightly that even an administrator could not read it.
- The recovery steps did not fail cleanly, which is the worst part. **The old token file
  was still sitting at the old location, and it still looked perfectly valid.** Anyone
  following the recovery steps would read a real-looking token, be told it was rejected,
  and have nothing at all pointing at why. It had expired ten minutes after it was
  written, hours earlier.
- Credentials now live in **one fixed folder that never moves**, no matter where the
  database goes.

## The quiet problem underneath

- The app protects saved credentials — API keys for outside services, the server's own
  login password — by encrypting them with a key kept in a file. If that key file is
  missing, the app **created a brand new one and carried on.**
- A new key cannot read anything the old key protected. So the app would then find that
  it could not decrypt the saved credentials. Its response was to recover the four it
  could find written down elsewhere, and **silently delete every other one.**
- The app exited normally and looked completely healthy. The only trace was, weeks
  later, a credential that mysteriously needed entering again. This had already happened
  once: two saved secrets needed re-entering after the drive move, and it was never
  clear why.
- There was a second, worse version of the same flaw. If the key file was present but
  could not be read for any reason at all, the app took that as "there is no key" and
  **wrote a new one straight over the top of the real one.** That is not recoverable.
  The key is gone, and everything it protected is unreadable for good.

## What changed

- Credentials have a fixed home that does not depend on the database. The database can
  be moved between drives as often as anyone likes; the recovery steps and the
  permissions that go with them keep working untouched.
- The app now **refuses to start** if the encryption key is missing while encrypted
  credentials still exist, and says which file to put back. Refusing to start is
  deliberate: it is the last moment at which the credentials can still be saved.
- The app will **never again write over a key file it failed to read.** Any problem
  reading the key is now treated as a reason to stop, not a reason to replace it.
- Servers that have already been through the drive move keep working. The app still
  looks in the old location, uses the key it finds there, and logs a message saying
  where to move the file. It does not copy it — one secret in two places is one more
  place it can leak from.

## What still needs a person

- Two leftover token files should be deleted, because both still answer and both are
  long expired. Anyone following the recovery steps could pick up either one and get an
  unexplained rejection. Removing them makes the old paths fail obviously instead of
  misleadingly.
- The encryption key file itself should be moved to its new home when convenient. Until
  then the server is relying on the fallback, and says so in its logs on every start-up.
