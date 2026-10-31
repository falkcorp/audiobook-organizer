<!-- file: docs/executive-summaries/2026-10-30-web-sessions-credential-epoch-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8ccc6e20-674c-4035-a0be-8dd41e78ce78 -->
<!-- last-edited: 2026-10-30 -->

# Changing or resetting a password now signs the person out everywhere on the web

## Executive Summary

Until now, if you changed your password, or an administrator reset it or switched your
account off, the browsers you were already signed in on stayed signed in. A session lasts a
day (a week if "remember me" was ticked, up to a month for one started from an
invitation), so someone who had borrowed or stolen a signed-in
browser could keep using it long after the owner changed the password to lock them out. The
phone apps already behaved correctly; the web pages did not. They do now.

## What changed

- **Every sign-in is tied to the password state it was made under.** If the password is
  changed, the account is reset with an administrator's link, or the account is switched off
  or back on, every earlier sign-in stops working at that moment, on the very next click.
- **Including a sign-in that was in progress.** Someone who typed the old password just as it
  was being changed gets a page that does not work; it is refused the first time it is used.
- **You stay signed in on the browser where you changed your own password.** Every other
  browser and phone is signed out. An administrator resetting someone else's password does
  not keep anyone signed in.
- **Switching an account back on does not bring old sign-ins back.** Only a fresh sign-in
  works.
- **A one-time reset link still works.** The person who follows it is signed in, and can then
  choose their own password and stay signed in.
- **Open live pages end too.** A page that keeps a live connection open (progress updates)
  is cut off on the same trigger.

## What to expect after the update

People who changed their password or had their account switched off since 19 October will
be asked to sign in once more, because their existing sign-ins have no record of the state
they were made under. Everyone else is not signed out.

## What this does not do

An administrator's reset link signs everyone out but does not make the old password stop
working: until the person picks a new one, whoever knows the old password can still sign in.
That is a separate decision that is still open (a "compromised account" reset).
