<!-- file: docs/executive-summaries/2026-10-20-cf-service-token-browser-recovery-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d1f9a3c-48e2-4b07-8c5d-2a9e7f0b3d14 -->
<!-- last-edited: 2026-10-20 -->

# A phone browser stuck on the login page now fixes itself

Branch `fix/cf-service-token-browser-recovery`, PR not yet opened. No separate spec.

## Executive Summary

- **The outage.** After signing in to the AudioBooth app with single sign-on,
  Safari on that phone could no longer reach the web app. Safari showed the
  login page and no sign-in worked, and a second AudioBooth sign-in was refused
  too.
- **Why it happened.** Cloudflare, the service in front of this site, gives out
  a small "I am signed in" cookie. The app proves who it is with a device token
  (a machine credential that does not name a person). The app's sign-in sheet
  shares cookies with Safari, so the cookie Cloudflare gave the device token
  replaced the one Safari held for the person. Every later Safari visit then
  looked, to the server, like a device and not like anyone's account.
- **What the server now does.** When a browser opens a page and Cloudflare says
  the visitor is a device token, the server answers with a small page that
  clears the bad cookie and sends the browser straight back to the same address.
  That second visit carries no device token, so Cloudflare asks the person to
  sign in the usual way (whichever login methods it is set up with) and the web
  app loads.
- **Never a loop.** The return trip is marked. If a browser comes back marked
  and is still treated as a device, the server shows a plain message with a link
  to Cloudflare's sign-out page instead of trying again. A person who comes back
  marked is sent to the clean address.
- **The app's own sign-in heals the same way.** The sign-in step that opens in
  the browser used to retry once and give up when the bad cookie was still
  there. It now clears the cookie and retries, and only gives up after two
  tries.
- **What it leaves alone.** Only pages opened in a browser are touched. The
  app's own requests, music streaming, the Watch app, background requests from
  the web app itself, and anything using an API key are never redirected, and a
  device token is never treated as a person's account.
- **Better diagnostics.** The server's debug log now reports the length of the
  Cloudflare sign-in cookie on its own, instead of mixing it with an unrelated
  Cloudflare bot-check cookie.
- **Not yet proven on a phone.** Automated tests with made-up data cover each
  step, but nobody has run it against the real Cloudflare setup on an iPhone.
  A follow-up task is filed to consider turning on Cloudflare's stricter
  device-token setting once streaming and the Watch app are checked with it.

## Browser stuck on the login page

**What it was.** The cookie that proves a person is signed in could be
overwritten by the one given to the app's device token.

**Why it mattered.** The owner was locked out of the web app on a phone, and
the app's single sign-on could not recover, because a simple redirect cannot
remove a cookie.

**The fix.** A short page, shown only for a page opened in a browser, clears
the bad cookie and reloads the address so Cloudflare asks for the person
again, with a marker and an error page so it can never repeat forever.

## The app's single sign-on step

**What it was.** When the sign-in page opened by the app still looked like a
device token, the server redirected once and then gave up, but a redirect
cannot remove a cookie, so the second try failed the same way.

**Why it mattered.** Single sign-on could not recover for anyone whose browser
held the bad cookie, and they had to clear it by hand.

**The fix.** The sign-in step now clears the cookie and retries up to two times,
then reports a clear failure to the app instead of looping.

## Diagnostic log

**What it was.** The debug log lumped every Cloudflare cookie into one length,
including an unrelated bot-check cookie that most requests carry.

**Why it mattered.** It could not answer the question the log exists for: did
the sign-in cookie arrive with the request.

**The fix.** The sign-in cookie's length is now logged on its own.
