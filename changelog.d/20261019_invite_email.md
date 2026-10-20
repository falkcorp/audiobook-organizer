### Added

#### Emailed invitations with the guest Cloudflare headers and an accept page

The Users page can now email an invitation. **Create invite** takes an optional
email address and a "Send email" checkbox, and pending invites can be resent
(at most once a minute per invite, with a per-admin cap). The email carries the
guest's own invite link, the shared guest Cloudflare service-token header pair
when one is configured, and the AudioBooth setup steps. If sending fails, the
admin still gets the link to hand over.

The link opens a new public page, `/invite/:token`, backed by
`GET /api/v1/auth/invite/:token`. It shows the username, expiry and header
steps only for a valid, unused, unexpired token, answers every other token
with the same 404, and is rate-limited together with `POST /auth/accept-invite`.

Mail goes through a new `internal/mail` SMTP sender: plain SMTP to a local relay
with no AUTH and no TLS works; STARTTLS is used when offered (except to loopback
with no username); AUTH PLAIN is sent only over TLS or to loopback; port 465
means implicit TLS; 15-second timeouts; multipart text and HTML with
header-injection checks.

Settings are environment-only, refused by PUT /config and never returned by
GET /config: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`,
`SMTP_FROM`, `PUBLIC_BASE_URL`, `INVITE_CF_ACCESS_CLIENT_ID`,
`INVITE_CF_ACCESS_CLIENT_SECRET`, `CF_ACCOUNT_ID`, `CF_GUESTS_POLICY_ID`,
`CF_POLICY_API_TOKEN`.

#### Accepting an emailed invite sets the email, allows SSO, and can update one Cloudflare policy

When the invitation was emailed to its address, accepting it sets that address
on the new account. When the invite was also created by an active admin, the
address may sign in with SSO (a stored grant consulted next to
`OAUTH_ALLOWED_EMAILS`). With the three `CF_*` settings, the address is also
appended to one reusable Cloudflare Access policy, keeping every other field.
The outcome is shown under "Accepted invites"; a Cloudflare failure never fails
the accept. A copied link, or an invite from a non-admin, creates the account
without these grants and says why. Deactivating the user or changing their
email removes the grant and, when the sync added it, the policy entry.

### Security

#### SSO auto-linking by email uses only verified addresses; emails are unique

An SSO login is now linked to an existing account by email only when that
address is verified: bound by an emailed invite, confirmed by an identity
provider, or set by first-run setup or bootstrap from now on. A self-set
profile email never matches. When an identity-provider-verified login finds its
address held unverified by other accounts, the verified login wins: in one
write the address is cleared from those accounts (they keep their accounts) and
a new account is created for the login; this is logged and recorded as an
`auth_email_displaced` audit activity entry with the account ids. A one-time
rebuild at startup (`system:backfill:user_verified_email_v2_done`) keeps only
addresses with a record of proof (an accepted-invite grant or a linked
identity recorded with that email) and drops every other key; people who
already sign in through SSO resolve by their identity link and are unaffected.
User emails are now unique case-insensitively; PATCH /auth/me answers 409 for
an address another account has.
