<!-- file: docs/executive-summaries/2026-10-30-admin-routes-auth-off-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2b9c4e71-5d83-4f06-a1e8-30c7d95b4a12 -->
<!-- last-edited: 2026-10-30 -->

# Turning login off no longer locks you out of the read-only admin pages, and still cannot be used to wipe the library

## Executive Summary

The app can be run with login switched off, for a private test machine. In that
mode most of the app simply works, but every page marked "administrators only" used
to refuse everyone, because nobody is signed in to be an administrator. This change
decides, page by page, which of those pages should open when login is off, and which
must never open.

## What changed

- **Read-only admin pages now open without a login.** The cache key listing and the
  four read pages of the admin debug tool only show information, so they work in a
  no-login run.
- **Anything that changes or deletes data still needs a signed-in administrator.**
  That covers resetting the store, the factory reset, the debug tool's edits and their
  undo, and the old retired wipe and cache-flush buttons. Without a login these still
  answer "authentication required", so someone who can reach the machine over the
  network cannot wipe it just because login is off.
- **The guest access token pages stay locked too.** They list guests and talk to
  Cloudflare, so they are treated like passwords, not like status pages.
- **With login on, nothing changes.** Anonymous callers are refused, signed-in
  non-administrators get "forbidden", administrators get through.
- **A new test makes this a rule.** Every administrators-only page has to be on a
  reviewed list, marked either open-when-login-is-off or not, so a new admin page
  cannot be added without someone deciding which it is.
