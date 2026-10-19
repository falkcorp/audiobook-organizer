<!-- file: docs/executive-summaries/2026-10-19-abs-sso-two-hop-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: f9284f41-8fc0-48da-a93d-763d1a975a5e -->
<!-- last-edited: 2026-10-19 -->

# Signing in to the AudioBooth app with single sign-on no longer crashes it

Branch `fix/abs-oidc-two-hop`, PR not yet merged. No separate spec.

## Executive Summary

- **The crash is fixed.** Tapping "Login with OpenId" in AudioBooth crashed the
  app every time. The app asks the server where to sign in and opens the answer
  in a browser; the server answered with an address the browser cannot open,
  and the app crashed on it. The server now always answers with a normal web
  page on this server.
- **Signing in is now two steps, like a real Audiobookshelf server.** The app
  first registers a sign-in request with the server. The browser then opens
  that request, where the person is signed in through the site's existing
  Cloudflare login, and sees a page that asks "Sign in to AudioBooth as
  <account>?" with Allow and Deny buttons. Only Allow sends the app back with
  a one-time code it can exchange for a session.
- **Why there is an Allow button.** Nothing ties the browser to the phone that
  started the sign-in. Without the button, anyone who started a sign-in could
  send the link to someone else, and simply opening it would sign them in on
  the first person's behalf. With it, nothing happens until the account owner
  agrees on a page that names the app and the account.
- **One person or group cannot jam sign-ins for everyone else.** Pending
  sign-ins expire after five minutes. Each Cloudflare credential has a limit on
  how many it can have pending (generous for the shared group tokens, small
  for one person) and on how fast it can start them. When the server's list is
  full it drops the oldest requests of whoever has the most, so a flood only
  pushes out its own requests.
- **Not yet proven in production.** On current iPhones the app sends its
  Cloudflare group token to the browser as well. The server is built to step
  around that (it reloads the page once without the token, so Cloudflare asks
  for the person). How Cloudflare chooses between the token and the person's
  own login has not been tried against the live site.
- **Verification.** Automated tests with made-up data cover the whole sign-in
  from first request to session, wrong and replayed codes, expired requests,
  the Allow/Deny page and its protections, and the per-credential limits.
  Every key check was confirmed by deliberately breaking it and watching a test
  fail (35 of 35 caught). The Allow button's browser behaviour was checked in
  real WebKit and Chromium, which caught a setting that would have made every
  Allow fail.

## What was wrong

The app makes its first sign-in request itself, carrying only its Cloudflare
group token, and opens the server's reply in a secure browser window. That
window accepts only a web address. The server replied straight away with the
app's own private address (the one the app uses to receive the result),
usually carrying "access denied", because the group token names no person. The
app passed that to the browser window and crashed. Production showed this on
every attempt.

## How it works now

The first request now only records the sign-in (which app, where to send the
answer, and the app's one-time proof) and replies with a web page on this
server. Problems with that first request come back as a short plain message
the app displays, never as a redirect.

In the browser, Cloudflare signs the person in as it does for the website. The
server shows the Allow/Deny page. Loading the page changes nothing. Allow is
accepted only from that page, for that person and that sign-in. Then the app
gets a code that works once, for one minute, and only together with the proof
the app kept to itself.

## Security review

An automated security review of this change ran three rounds, and each round's
findings were fixed in this PR:

1. Add the Allow page, and limit how many sign-ins a caller can leave pending.
2. Do not let one shared group token's devices push each other out.
3. Bound each credential's pending sign-ins, make a full list drop the largest
   holder's oldest entries, and stop trusting a client-address header that
   cannot be verified.
