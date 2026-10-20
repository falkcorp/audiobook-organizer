<!-- file: docs/executive-summaries/2026-10-19-invite-email-executive-summary.md -->
<!-- version: 1.3.0 -->
<!-- guid: dcdc41d2-cf49-46aa-bf82-9b7b4fe9160a -->
<!-- last-edited: 2026-10-19 -->

# Inviting someone now sends them everything they need, by email

Branch `feat/invite-email`, PR not yet opened. No separate spec.

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
- **No account can share an email with another.** Email addresses are now
  unique, ignoring capitalization.
- **Secrets stay on the server.** The mail password, the Cloudflare key and
  the shared connection secret are set only in the server's environment, are
  never shown in the settings screens or logs, and cannot be changed through
  the web interface.

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
emailed invite, confirmed by Google or GitHub, or set during first-run setup
from now on. When a Google- or GitHub-verified login finds its address typed
into other accounts' profiles, the verified person wins: those accounts keep
working but lose the address, a new account is created for the login, and an
entry naming the affected accounts goes into the admin's audit log. An
account with any proof of its address is never displaced. A one-time rebuild
at startup keeps only addresses with a record of how they were proven, so an
address someone squatted before this update is not grandfathered in. The
owner's own email and the addresses on the operator's allow list count as
proof for the one active account that holds them, so the owner's first single
sign-on reaches the owner's account. Each account now remembers where its
email came from (setup, an admin, an invite, Google or GitHub, or the person
typing it in), and the allow list never vouches for an address someone typed
into their own profile: if a person puts an invited guest's address on their
own account before the guest signs in, the guest's real Google or GitHub
sign-in takes the address back; taking an address off the list withdraws
that trust right away, and two accounts holding one listed address get
neither. People who already sign in with Google or GitHub are found
through their existing sign-in link, not their email, so they are not affected.
The trusted list stays where older versions of the server look for it, so
rolling back cannot reopen the hole.
