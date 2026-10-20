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
with the same 404. It and `POST /auth/accept-invite` share a limiter with three
layers: per client, per token (keys are hashed), and global. The client is
taken from `CF-Connecting-IP` only when the request carried a verified
Cloudflare Access assertion and came from a local peer; otherwise the peer
address is used. The accept body is capped at 4 KiB (413 above it) and parsed
once for both the limiter and the handler. Invite tokens are redacted from
request logs, trace span attributes (`url.path`) and error logs.

Mail goes through a new `internal/mail` SMTP sender: plain SMTP with no AUTH
and no TLS works only to a loopback relay; any other host must offer STARTTLS
(or use port 465 for implicit TLS) or the send is refused before `MAIL FROM`,
with no plaintext fallback; AUTH PLAIN is sent only over TLS or to loopback;
15-second timeouts; multipart text and HTML with header-injection checks.
Sending and resending are refused unless both a mail sender and an absolute
http(s) `PUBLIC_BASE_URL` are configured (resend answers 503
`PUBLIC_URL_NOT_CONFIGURED` for the latter), and the invite list reports which
one is missing as `mail_unavailable_reason`.

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
The policy update sends only the policy's writable fields, then reads the
policy back to confirm the address landed (or is gone), retrying from a fresh
read up to three times; an update that was sent but could not be confirmed is
reported to the admin rather than dropped. The outcome is shown under
"Accepted invites"; a Cloudflare failure never fails the accept. A copied link
binds no email and grants nothing; an emailed invite from a non-admin binds no
email either (`creator_not_admin`). Both say why. Deactivating the user or
changing their email removes the grant and, when the sync added it, the policy
entry.

#### Admins can set or clear another user's email

`PUT /api/v1/users/:id/email` (admin role and `users.manage`) sets or clears a
user's email from the Users page. An address set this way counts as verified.
Changing it removes the old address's SSO grant and, when the sync added it,
its Cloudflare policy entry. This is the way to free an address that blocks an
invite, or to confirm an address an account already holds.

### Security

#### SSO auto-linking by email uses only verified addresses; emails are unique

An SSO login is now linked to an existing account by email only when that
address is proven for the account. Each user records where its email came from
(`email_source`: `setup`, `admin`, `invite`, `idp`, `self`, or `invite_link`),
and the store sets it on every write path, ignoring a caller's value. An
address is proven when its source is `setup`, `admin`, `invite` or `idp`, when
an accepted-invite grant names the account, or when a linked identity recorded
that email. The lookup checks this against the user rows at the time of the
login; the email index is only a cache that the lookup corrects. A self-set
profile email, or one bound by a copied invite link, never matches.

At every start the store stamps a source on rows written before this field: an
identity with that email gives `idp`; an invite that created the account gives
`invite` when an admin sent it, else `invite_link`; an email index entry
written by account creation gives `setup`; anything else is `self`. Admin
accounts stamped `self` are logged by id at warning level, because an SSO
login with that address will take it from them; an admin can set the address
for them to keep it.

When an identity-provider-verified login, or the acceptance of an invite an
admin emailed, finds its address held unproven by other accounts, the verified
claim wins: in one write the address is cleared from those accounts (they keep
their accounts), it is recorded as an `auth_email_displaced` audit activity
entry with the account ids, and accepted invites list them as
`displaced_user_ids`. A holder with any proof is never displaced, and an
unreadable user row refuses the displacement.

User emails are unique case-insensitively. Creating an invite for an address
another account holds answers 409. PATCH /auth/me answers a generic 400 for
an address another account has, so a signed-in user cannot test which
addresses have accounts; the detail is logged by user id.

#### Bootstrap token exchange creates nothing without a valid token

`POST /api/v1/auth/bootstrap` found or created the admin account before it
checked the token, so when no active admin existed, any request with a
non-empty token created one (the requester never received its password or a
key), and the real recovery then reused that admin and returned no password. The token is now checked first (constant-time hash compare; a missing
or unreadable stored expiry counts as expired), the admin is found or created
only after that, and the token is spent last, all under one lock; a failed
admin creation still leaves the token usable. Only an active admin is reused;
a new one takes the first free name of `admin`, `admin-2`, and so on, with no
email (the old fixed `admin@localhost` collided with the uniqueness rule). Its
generated password is now 28 base32 symbols from `crypto/rand` (140 bits);
the old three-word passphrase had about 28 bits.
