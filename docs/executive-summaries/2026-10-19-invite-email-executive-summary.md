<!-- file: docs/executive-summaries/2026-10-19-invite-email-executive-summary.md -->
<!-- version: 1.10.0 -->
<!-- guid: dcdc41d2-cf49-46aa-bf82-9b7b4fe9160a -->
<!-- last-edited: 2026-10-20 -->

# Inviting someone now sends them everything they need, by email

Branch `feat/invite-email`, PR #4090. No separate spec.

## Executive Summary

- **Invitations can be emailed.** When the owner adds someone on the Users
  page, the server can send that person one email with their own sign-up
  link, the two connection lines the AudioBooth phone app needs to get past
  Cloudflare (the security service in front of the site), and step-by-step
  setup instructions. If the email cannot be sent, the owner still gets the
  link to pass on by hand.
- **The guest signs up on a new page.** The link opens a page where the guest
  picks their own password. The connection lines are shown there too, but
  only to someone holding a valid, unused, unexpired link.
- **An emailed invite can also open single sign-on and browser access.**
  When the invitation went to that email address and was sent by an
  administrator, the guest can also sign in with Google or GitHub, and the
  server can add the address to the one Cloudflare rule that lets browsers in.
  If that Cloudflare step fails, the account is still created and the Users
  page says to add the address by hand.
- **Access is taken back when it should be.** Deactivating the guest or
  changing their email removes the sign-on permission and, if the server added
  it, the Cloudflare entry, without removing an address someone else still
  legitimately uses.
- **A sign-in takeover was closed before it shipped.** Linking a Google or
  GitHub login to an existing account by email now only trusts addresses that
  were actually verified. While building this feature, a version of the code
  let anyone type another person's address into their own profile and capture
  that person's first single sign-on; that version never reached production.
- **Only one account can hold an address as verified.** Two accounts may
  both type the same address into their profiles, but only one account can
  have it verified, and only a verified address links a Google or GitHub
  sign-in. Look-alike spellings (for example a special Unicode "K" in place of
  "k") count as the same address. An administrator can now set or clear
  another person's email from the Users page.
- **Invite links are not handed to automation.** Holding an invite link is
  enough to accept it, so the list of pending invites never shows the link to
  an API key used by scripts, and shows the link of an emailed invite only to
  an administrator signed in to the web page. Revoking or resending an invite
  now uses a separate reference number that cannot be used to accept it.
- **Secrets stay on the server.** The mail password and the Cloudflare key
  are set only in the server's environment, are never shown in the settings
  screens, admin responses or logs, and cannot be changed through the web
  interface. The shared connection secret is set the same way and is likewise
  never shown in settings, admin responses or logs, but it is sent on purpose
  to the guest: it is in the invitation email and on the sign-up page for a
  live link, because the guest's app needs it.
- **An emergency-access gap was closed.** The one-time recovery token used to
  regain access to a locked-out server was checked only after the server had
  looked for, and if needed created, an administrator account. In the
  released version that could only happen when the server had no
  administrator account at all (locked ones counted), and the server's own
  start-up step normally created one first, so in practice the window was
  small. Nobody who triggered it received the account's password or a key.
  Nothing is created now without the correct token, the generated
  administrator password is far stronger, and the start-up step no longer
  creates an administrator nobody has the password for.

## Emailed invitations and the sign-up page

**What it was.** Adding a guest meant copying an invite code out of the Users
page and separately sending them the two Cloudflare connection lines and the
app instructions by hand.

**Why it mattered.** Every new guest needed several hand-written messages, and
the connection secret travelled through whatever channel was handy.

**The fix.** The invite dialog takes an email address and a "Send email" box.
The server sends one plain-text-and-HTML email through any standard mail
server, including a local relay with no password. A resend button is limited
to once a minute per invite. The new sign-up page lets the guest set a password
and shows the connection lines only for a live invite link.

## Single sign-on and the Cloudflare browser rule

**What it was.** A guest who wanted to use the website in a browser, or sign in
with Google, had to be added by hand to two separate allow lists.

**Why it mattered.** The manual steps were easy to forget, and both lists are
security-sensitive: adding the wrong address lets a stranger in.

**The fix.** Accepting an invite gives these grants only when holding the link
proves the guest controls the address (the invite was emailed to it) and the
person who sent it is an administrator. The Cloudflare change edits exactly one
pre-agreed rule, keeps everything else in it unchanged, and never fails the
sign-up. The Users page shows the result for each accepted invite.

## Taking access back

**What it was.** Nothing removed an invited guest's sign-on or browser access
when their account was turned off or their address changed.

**Why it mattered.** A deactivated guest could have kept browser access through
Cloudflare indefinitely.

**The fix.** Deactivation and email changes remove the sign-on permission and,
when the server added it, the Cloudflare entry. An address is kept when it is
the owner's, on the operator's own allow list, or verified for another active
account. A removal that races a still-running Cloudflare update is caught, so
the address is not left behind.

## Verified emails for sign-on linking

**What it was.** When someone signs in with Google or GitHub for the first
time, the server links them to an existing account with the same email. Midway
through this work, profile email changes started feeding that lookup.

**Why it mattered.** Anyone could have typed another person's address into
their own profile and then received that person's account access the first
time they signed in with Google or GitHub.

**The fix.** Only verified addresses are used for linking: ones bound by an
emailed invite from an administrator, confirmed by Google or GitHub, set by an
administrator, or set during first-run setup. Each account now remembers where
its email came from, and only that counts: an address typed into a profile is
never verified, whatever else the account has. The server checks this against
the accounts themselves at the moment of each sign-in, not against a saved
list that could go stale. The profile page accepts any address and simply
records it as unverified, so it cannot be used to find out which addresses
belong to someone. Accounts from before this update are labelled once, from the records
the server has: an address confirmed by Google or GitHub, one bound by an
invite an administrator sent, or one set when the account was created counts
as verified; anything else counts as typed in by the person. Each old
account is judged by the strongest record it has, so an account created with
the address that later also signed in with Google keeps it. If two old
accounts would both count as verified for one address, only the one with the
strongest record keeps it; when they are equally strong, the one sign-on
reached before the update keeps it, and the server logs the others. An address someone
squatted before this update is therefore not grandfathered in.

When a Google- or GitHub-verified sign-in, or the acceptance of an invite an
administrator emailed, finds its address typed into other accounts' profiles,
the verified person wins: those accounts keep working but lose the address,
and an entry naming them goes into the admin's audit log. An account that has
the address verified is never displaced. People who already sign in with
Google or GitHub are found through their existing sign-in link, not their
email, so they are not affected.

**One thing to check before deploying.** If an administrator's own address
was last changed through their profile page, it is labelled as typed in, and
the first Google or GitHub sign-in with that address would move it to a new
account. The server logs the ids of any such administrator accounts at
startup. Do not fix this by setting the address for them on the Users page:
that makes the account the verified holder, which is wrong when the same
person already signs in through another account. Leave it as it is. If the
log says another account already has the same address verified, it names
both accounts and which one is the verified holder: do not clear that
holder's email, and leave both accounts alone until they can be merged.

**A second setting, set by this deploy.** The invite pages limit how often
one visitor can try a link. The Cloudflare tunnel reaches the server through
three small relay machines on the home network, so every visitor arrives from
one of those three addresses. `TUNNEL_CONNECTOR_ADDRS` names them, which lets
the server read the real visitor address that Cloudflare attaches and give
each visitor their own limit. It ships with this deploy. Without it, each
relay would get one limit shared by every guest who came through it. Faking
Cloudflare's headers does not let anyone else pick or escape their limit.
