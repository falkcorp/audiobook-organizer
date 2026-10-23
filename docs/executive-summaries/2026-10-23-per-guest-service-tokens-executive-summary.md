<!-- file: docs/executive-summaries/2026-10-23-per-guest-service-tokens-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: e7c2b4a9-5d18-4f63-8b0e-9a1d3c6f2e58 -->
<!-- last-edited: 2026-10-23 -->

# Guests get their own phone-app key

## Executive Summary

Guests who listen through the phone app cannot use Cloudflare's browser
sign-in, so the app needs a key of its own to get through. Until now the plan
was a key shared by everyone, which the 21 October hardening removed because
anyone holding an invitation link could read it. This change gives **each guest
a private key**, created for them when they accept their invitation and shown
to them once.

## What changed

- When a guest accepts an invitation, the server asks Cloudflare for a key made
  just for that guest and lets only that key into the library's phone-app
  entrance. The guest sees the two values once, on the page, with the steps to
  type them into the app.
- The server remembers only a scrambled fingerprint of the secret, never the
  secret itself, so it cannot show it again or leak it from its own records.
- Deactivating a guest cuts off their key. An admin can also cut off any one
  guest from the Users page ("Phone app keys") without touching anyone else.
  The page lists each guest's key, when it was made, and when it was last used
  if Cloudflare says.
- Every creation and removal is written to the audit log (who and which key,
  never the secret).
- It is switched off until the owner turns it on, and a failed key never stops
  an account being created: the guest can still use the website.

## Protections

- The owner's own Cloudflare keys cannot be touched by this feature: the code
  refuses their ids outright, and only keys the server itself made for guests
  can be removed.
- If Cloudflare refuses (the server's Cloudflare login lacks permission), the
  server does nothing and tells the admin exactly which permission is missing.

## What the owner needs to do

Add one permission to the server's existing Cloudflare API token:
**Account > Access: Service Tokens > Edit**. Then set `CF_GUEST_SERVICE_TOKENS`
and `CF_SERVICE_AUTH_POLICY_ID` (see
`docs/operations/cloudflare-guest-service-tokens.md`).

## Not covered yet

- A guest whose key could not be created at accept time cannot be given one
  later without further work.
- A guest who is reactivated does not get a new key automatically.
- It has not been exercised against the real Cloudflare service, only against
  recorded request shapes.
