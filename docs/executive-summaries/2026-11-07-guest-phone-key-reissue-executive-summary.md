<!-- file: docs/executive-summaries/2026-11-07-guest-phone-key-reissue-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 25b562ce-0a87-4050-beae-d5b3f1f293ae -->
<!-- last-edited: 2026-11-07 -->

# Admins can now give a guest a fresh phone-app key

**Date:** 7 November 2026

## Executive Summary

- Each guest on the phone app has their own key. Until now, if a guest lost it,
  or it was revoked, or it was never created, the only fix was to do it by hand
  in the Cloudflare dashboard.
- The Users page now has a button for it. On a guest who has a key it reads
  "Reissue key". On an active user with a verified email and no key yet it
  reads "Issue key".
- Pressing it cuts off the old key straight away, makes a new one, and emails it
  to the guest at their verified address. The admin never sees the key.
- If the email cannot be sent, the new key is cancelled again so no working key
  is left that nobody has. The row says what went wrong and the admin can try
  again once mail works.
- The button refuses up front, before touching anything, if mail or the
  Cloudflare settings are not set up, so the old key is never cut off for
  nothing.
- Only the owner can use it, the same as Revoke.

## Why it mattered

A phone-app key is the only way a guest's app gets past the Cloudflare gate. A
lost key meant a guest locked out until someone with Cloudflare access did a
manual fix, and the manual fix is easy to get wrong (a stray old key that still
works, or a new key that was never added to the access rule).

The new button does the same steps in a fixed order every time and records that
it happened, without writing the key anywhere an admin or a log can read it.

## What did not change

- Joining a guest by invitation works exactly as before, including the one-time
  key on the page the guest sees.
- Revoking a key works as before; the Revoke dialog now points to Reissue for
  giving the guest a new one.
- No data was changed or migrated. A reissue reuses the guest's existing key
  record.
