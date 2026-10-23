<!-- file: docs/operations/cloudflare-guest-service-tokens.md -->
<!-- version: 1.0.1 -->
<!-- guid: 3a9d6f21-8c74-4e15-b0a2-6d1f4c8e7b95 -->
<!-- last-edited: 2026-10-23 -->

# Per-guest Cloudflare service tokens (phone-app access for guests)

A guest on the Audiobookshelf-compatible phone app cannot do Cloudflare's
browser login, so the app has to send a Cloudflare Access **service token**
with every request. This feature gives each guest their **own** service token,
minted when they accept their invitation, instead of any shared credential.

It is **off by default**. With the flag off the server makes no Cloudflare
calls for this and the accept response has no new fields.

## What the owner must add first: one extra API permission

The server already talks to Cloudflare with `CF_POLICY_API_TOKEN` to add guests'
emails to the guest Access policy. Minting service tokens needs one more
permission on **that same API token**:

> **Account > Access: Service Tokens > Edit**
>
> (In the Cloudflare API documentation the permission is named
> "Access: Service Tokens Write".)

The policy edits already need **Account > Access: Apps and Policies > Edit**.
Keep it.

Without the new permission Cloudflare answers HTTP 403 and the server **fails
closed**: no token is created, no policy rule is added, and the admin Users
page and the logs say that `CF_POLICY_API_TOKEN` is missing the permission. The
guest is told only that phone-app access could not be set up automatically;
their account and browser sign-in still work.

## Settings (environment only)

| Variable | Default | Meaning |
|---|---|---|
| `CF_GUEST_SERVICE_TOKENS` | off | Turns the feature on (`cf_guest_service_tokens`). |
| `CF_SERVICE_AUTH_POLICY_ID` | none | Id of the non-identity ("books") Access policy that admits service tokens. The server adds one rule per guest token to it. Required when the flag is on. |
| `INVITE_EMAIL_SERVICE_TOKEN` | off | Also email the key to the guest's verified address after they accept. Off by default because email is not a safe place for a secret. |

The feature also needs the existing `CF_ACCOUNT_ID` and `CF_POLICY_API_TOKEN`.
If the flag is on and any of the three is missing the server logs a warning at
startup, the Users page shows it, and nothing is minted.

## How it works

1. An admin invites a guest by email (the invitation must be emailed, and the
   creator must be an active admin: the same gate as the guest-policy sync).
2. The guest opens the link and chooses a password. The server creates the
   account, then asks Cloudflare for a service token named `aorg-guest-<user id>`
   and adds **one rule for that token id** to the books policy.
3. The accept page shows the guest the client id and client secret **once**,
   with the two header names to enter in the app (`CF-Access-Client-Id`,
   `CF-Access-Client-Secret`). The response is sent with `Cache-Control:
   no-store`.
4. The server keeps only a SHA-256 hash of the secret, the Cloudflare token id,
   and the guest's user id. The secret is never logged, audited or stored in
   readable form, and cannot be shown again.

The books policy is never given `any_valid_service_token`: each rule names one
token id, so revoking one guest touches nothing else.

## Revoking

- **Deactivating a guest** revokes their token and removes their rule.
- **Admin action:** Users page > Phone app keys > Revoke (or
  `POST /api/v1/admin/guest-service-tokens/<user id>/revoke`). It runs
  synchronously, so you see whether Cloudflare confirmed it. A revoke that
  fails is marked "Revoke failed" and can be retried.
- The list (`GET /api/v1/admin/guest-service-tokens`) shows token id, guest,
  status, created time, and last seen when Cloudflare reports it.
- Changing a guest's email address does **not** revoke the token.
- There is currently no user-delete path in the server, so there is nothing to
  hook for deletion; the same revoker is the one to call when one is added.
- A guest who is reactivated after deactivation does **not** get a new key
  automatically.

Every mint and revoke (and every failure) is written to the audit log with the
guest's user id and the token id, never the secret.

## Safety rails

- The owner's own service tokens (ids starting `90a520fb`, `56ffe232`,
  `790ef191`) are **never touched**. Both Cloudflare clients refuse those ids
  before any request, only ids recorded in this server's own store are ever
  revoked, and a token is only deleted after a lookup confirms its name starts
  with `aorg-guest-`.
- Cloudflare responses are treated as data: ids and secrets are validated
  against strict patterns before use.
- Tokens are created with Cloudflare's `forever` duration, because there is no
  renewal flow and an expiry would silently cut a guest off. Revoking is the
  lifecycle control.

## Known limits

- If minting fails at accept time (for example the 403 above) the guest has no
  self-service retry, and an account that already accepted cannot be issued a
  key later yet. Fix the permission, then follow up (tracked in `todo.d`).
- Minting happens while the guest waits on the accept request and is bounded at
  45 seconds. The server's `--write-timeout` defaults to disabled; if you set
  it below 45s a slow Cloudflare call could cut the accept response short.
- Cloudflare's list endpoint is documented without `last_seen_at`; the column
  shows "Not reported" when it is absent.
- The code has been tested against recorded request/response shapes only; it
  has not been run against the live Cloudflare API.

## Rollback

1. Revoke each guest's key first (Users page, or the dashboard: delete service
   tokens named `aorg-guest-*` and remove their rules from the books policy).
2. Unset `CF_GUEST_SERVICE_TOKENS`. The stored `guestcftoken:` rows are inert.

## Orphan cleanup

If a mint was interrupted, a token named `aorg-guest-<user id>` can exist in
Cloudflare without a record here. It is not used by anything (no rule names it),
and can be deleted in the dashboard.
