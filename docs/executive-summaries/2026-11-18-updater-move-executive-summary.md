<!-- file: docs/executive-summaries/2026-11-18-updater-move-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2bbd9c34-fa59-491f-9943-fd06fa02de5f -->
<!-- last-edited: 2026-11-18 -->

# Rolling back across a big upgrade now fetches the right old version for you

**Date:** 18 November 2026. The self-updater moved into the shared core, and the
rollback command learned to fetch an older release by name.

## Executive Summary

- The part of the program that updates itself (checks for a new release,
  downloads it, swaps it in and keeps the old copy aside) moved into the shared
  core that other apps will use. For the people running this app nothing about
  updating changed: the same buttons, the same checks, the same swap, the same
  restart.
- The rollback command had one gap. If you upgraded across a change in how the
  library is stored, the old copy kept aside no longer understood the new
  storage, so rolling back was refused with no way forward. You can now ask for
  a specific older release (`make rollback-restore RELEASE=<version>`). The
  command fetches that release, runs it once to prove it is the right program at
  the right version, checks that it understands the storage of the backup being
  restored, and only then puts it in place as the copy kept aside.
- Every refusal on that path leaves things as they were. The fetched file is
  deleted, the existing kept-aside copy stays, and the running program is never
  touched.
- Nothing new is switched on for users. The new update routes and background
  checks exist in the core but are not connected until a later step.

## What you need to do

The rollback helper that installs the fetched release runs as the administrator
account on the server, so it only takes effect once the helper bundle is
reinstalled there and one setting (`stage_bin`) is added to the rollback
configuration. Until then the new command refuses before changing anything.

## What to keep in mind

- The fetched release comes from the public release page over HTTPS. This build
  has no signature check on it; the run-once proof and the storage comparison
  are the safeguards.
- The updater follows its own rule that a restore in progress blocks an update;
  the old in-memory check was rebuilt for that and is tested against it.
