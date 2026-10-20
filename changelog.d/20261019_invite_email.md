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
with the same 404. `POST /auth/accept-invite` answers a missing, used or
expired token, a username taken since the invite was made, and an invited
address another account already holds verified with one identical 400 (the
detail is logged), so the public route does not tell a caller which tokens
or addresses exist. Both routes share a limiter with three layers: per
client, per token (keys are hashed), and global. `CF-Connecting-IP` is
believed only from a peer listed in the new `TUNNEL_CONNECTOR_ADDRS`, or from
a local peer whose request carried a verified Cloudflare Access assertion;
a direct non-local peer is keyed on its own address. When no client address
can be established the per-client layer is skipped rather than shared, so
with `TUNNEL_CONNECTOR_ADDRS` unset tunnel traffic gets only the per-token and
global layers (logged once at start). The accept body is capped at 4 KiB (413
above it) and parsed once for both the limiter and the handler. Invite tokens
are redacted from request logs, trace span attributes (`url.path`) and error
logs.

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
`CF_POLICY_API_TOKEN`, `TUNNEL_CONNECTOR_ADDRS` (comma-separated addresses the
Cloudflare tunnel connector reaches this service from; set it to the
connector's address to restore per-client limits on tunnel traffic).

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
user's email from the Users page. An address set this way counts as verified,
so it is refused when another account already holds it verified, even when it
is already this user's address (re-setting it would make it verified twice);
clear it there or merge the accounts first. Changing it removes the old
address's SSO grant and, when the sync added it, its Cloudflare policy entry.

### Security

#### SSO auto-linking by email uses only verified addresses

An SSO login is now linked to an existing account by email only when that
address is proven for the account. Each user records where its email came from
(`email_source`: `setup`, `admin`, `invite`, `idp`, `self`, or `invite_link`),
and the store sets it on every write path, ignoring a caller's value. An
address is proven only when its source is `setup`, `admin`, `invite` or `idp`;
nothing else on the account counts, so an address typed in through the profile
(`self`) is never proven, even when the account has an older linked identity
or invite grant for it. An identity-provider login that confirms the account's
current address re-stamps it `idp`. The lookup checks this against the user
rows at the time of the login, and links the identity in the same locked store
operation, so the holder cannot change in between; the email index is only a
cache that the lookup corrects. Addresses are compared after Unicode NFKC
normalization and lowercasing everywhere (keys, uniqueness, the OAuth
allowlist, guest-access revocation, the Cloudflare policy list), so a
compatibility spelling such as a Kelvin sign (U+212A) for `k` is the same
address.

At every start the store stamps a source on rows written before this field,
the best one the row's history supports (the creation key outranks an
admin-sent invite, which outranks an identity): an email index entry written
by account creation gives `setup`; an invite that created the account gives
`invite` when an admin sent it, else `invite_link` (older accepts also wrote
the index entry, which then does not count as `setup`); an identity with that
email gives `idp`; anything else is `self`. A row with both the creation key
and an identity is therefore `setup`, not `idp`. Stamping never makes a second
verified holder: a row whose history points to a trusted source is stamped
`self` when another account already holds the address verified, or when
another old row also qualifies with a higher rank. On a tie for the highest
rank, the row the existing email index entry names keeps it (the account an
SSO login reached before this update); with no entry naming one of them, all
tied rows are demoted. Those ids are logged. Admin accounts stamped `self` are
logged by id at warning level: an SSO login with that address will take it
from them. The log says to leave it or merge the accounts with the
account-merge tool, not to set the address for them, which would make that
account the verified holder.

When an identity-provider-verified login, or the acceptance of an invite an
admin emailed, finds its address held unproven by other accounts, the verified
claim wins: in one write the address is cleared from those accounts (they keep
their accounts), it is recorded as an `auth_email_displaced` audit activity
entry with the account ids, and accepted invites list them as
`displaced_user_ids`. A verified holder is never displaced, and an unreadable
user row refuses the displacement.

At most one account may hold an address verified; that is the only email
uniqueness rule, compared after normalization. Every path that makes an
address verified refuses (or, for an SSO login, denies) when another account
already holds it verified. Addresses typed in through the profile are not
checked at all: PATCH /auth/me accepts any address, including one another
account holds verified, and records it unverified, so the profile form cannot
be used to learn which addresses have accounts. Creating an invite answers
409 only when another account holds the address verified (admin-only route).

#### Bootstrap token exchange creates nothing without a valid token

`POST /api/v1/auth/bootstrap` found or created the admin account before it
checked the token. On main an admin was created only when no account with an
admin role existed at all, whatever its status, and the startup read-only key
(`write_startup_readonly_key`, on by default) already created one at every
start when none existed; so the request path created an account only when no
admin-role account existed and that startup step was off or had failed. The
requester never received the account's password or a key. The token is now
checked first (constant-time hash compare; a missing or unreadable stored
expiry counts as expired), the admin is found or created only after that, and
the token is spent last, all under one lock; a failed admin creation still
leaves the token usable. Only an active admin is reused; a new one takes the
first free name of `admin`, `admin-2`, and so on, with no email (the old fixed
`admin@localhost` collided with the uniqueness rule). Its generated password
is now 28 base32 symbols from `crypto/rand` (140 bits); the old three-word
passphrase had about 28 bits. The bootstrap attempt limiter keys on the same
trusted client address as the invite limiter (IPv6 by /64), shares one bucket
for callers whose address cannot be established, drops ended windows, caps
tracked addresses at 10,000 and allows at most 100 attempts an hour across
all callers; a direct LAN or loopback caller with no Cloudflare markers keeps
its own 5-an-hour bucket outside that ceiling.

#### The startup read-only key no longer creates an admin

With `write_startup_readonly_key` on (the default), every start created an
admin when none existed, with a generated password nobody ever saw; first-run
setup then refused (an account existed) and bootstrap returned no password.
The startup key now uses an existing active admin only and is skipped, with a
log line, when there is none: a fresh install gets no startup read-only key
until first-run setup or bootstrap has made an admin, and the next start mints
it.
