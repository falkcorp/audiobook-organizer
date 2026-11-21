<!-- file: docs/executive-summaries/2026-11-21-login-timing-equal-work-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b22d978-63e3-4351-86a3-9a1f4a829282 -->
<!-- last-edited: 2026-11-21 -->

# A failed sign-in no longer hints at whether the username exists

**Date:** 21 November 2026. One pull request: the fix branch for sign-in timing.

## Executive Summary

- A test that checks sign-in timing failed once on a busy machine. It
  measured whether a wrong password for a real account takes longer than a
  sign-in for a name that does not exist. A difference would let someone
  find out which names have accounts just by timing the answers. The failure
  turned out to be noise from the busy machine, and the case it measures was
  already safe. The test now takes each case's fastest time over 25 rounds,
  in a different order each round, so a busy machine no longer trips it. It
  still fails at once if the protection is removed (20 out of 20 runs).
- Looking at other kinds of account turned up two real gaps. Accounts whose
  password was stored in the older format (bcrypt) answered at a noticeably
  different speed from a name with no account. On a busy server they could
  also get a different answer ("busy" against "wrong password"). Accounts
  whose stored password was damaged answered almost at once. Either one told
  a caller that the name exists. The gaps applied to all three ways of
  signing in: the web page, the phone apps, and the new core sign-in.
- Every password check now does the same work, whatever the name turns out
  to be: one check in the current format and one in the older format, each
  against the real stored password where there is one and against a stand-in
  otherwise. A busy server now turns all of them away alike.
- The price is about 63 milliseconds of extra processor time per sign-in on
  the development Mac, roughly double the previous cost. That cost lasts only
  while older-format passwords remain.
- To make them go away, signing in on the web page now upgrades an
  older-format password to the current format, as the phone-app sign-in
  already did. Each server start logs how many older-format passwords remain,
  as a count with no names. When the count reaches zero, the older-format
  check, and its cost, can be removed. That step is on the task list.
- Nothing changes for anyone signing in. Nobody's password stops working, and
  nobody has to reset it.

## What was checked

- Each kind of account was measured: real accounts in both formats, accounts
  with no password, damaged entries and unknown names. Each now does exactly
  the same work and gets a byte-for-byte identical refusal.
- Sixteen deliberate breakages of the new protection were each caught by a
  named failing test.
