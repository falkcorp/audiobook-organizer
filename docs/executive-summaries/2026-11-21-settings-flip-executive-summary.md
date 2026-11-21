<!-- file: docs/executive-summaries/2026-11-21-settings-flip-executive-summary.md -->
<!-- version: 0.1.0 -->
<!-- guid: 2d7383e0-3873-482c-a3ca-037bf97d1050 -->
<!-- last-edited: 2026-11-21 -->

# The settings flip: what it changes, what the rehearsal caught, and how to ship it

**Date:** 21 November 2026. **Changes:** the settings flip (#4949), shipped together with the schedule
declarations (#4950) and the log-purge schedule fix (#4951). Earlier summary of the same change:
[Settings and saved passwords move into the new core](2026-11-21-settings-move-to-core-executive-summary.md).

## Executive Summary

- **What the flip changes for the owner.** The service's settings and stored passwords move into the shared core
  the rest of the service already runs on. The Settings page looks and saves the same. Stored passwords are shown
  only as "set" or "not set". The server's settings file uses new names that all start with `ABO_`.
- **The first start converts the database, once.** On a full copy of production this took 55 to 75 minutes. The
  service does not answer during that time. After it, the older program refuses to open the database instead of
  misreading it.
- **The rehearsal caught a problem that would have hidden the whole library.** On the production copy, the new
  version started with no library folder configured. Covers, file moves and the jobs that work on the library
  would have refused to run. The fix keeps the library folder exactly where the old version stored it, and
  rehearsal checks now look for this.
- **The deploy needs two things from the owner first.** A one-time install of the rollback helpers on the server,
  and the settings file renamed to the new names. The deploy command refuses to run until both are done, and
  explains why.
- **Rollback means restoring the safety copy.** Swapping back to the old program is not enough. Going back
  restores the copy of the database the new version takes before it changes anything, and loses every change made
  after it.
- **Verified on a production copy before shipping.** The combined change is rehearsed on a full copy of production
  data. That rehearsal is the gate for merging.

## The lost library folder

- **What it was.** The old version keeps the library folder (where the audiobooks live) inside its saved settings.
  The new version looked for it in a new place that production never fills in. On the production copy, it started
  with the library folder blank.
- **Why it mattered.** The service would have come up "healthy" while every library feature quietly stopped:
  - book covers would fail to load;
  - the job that prunes old reports refused to run;
  - the download-client and iTunes connections would not have started.

  Nothing would have been deleted, but the library would have looked broken until someone noticed. The first
  rehearsal passed anyway, because nothing checked the library folder.
- **The fix.** The new version now reads and saves the library folder in the same place the old one did, until a
  later, separate change moves it on purpose. An old server setting that never actually set the folder now only
  produces a warning asking you to delete it. The rehearsal checks were extended in four ways:
  - every start of the new version must use the same library folder as the old one;
  - a library page must load;
  - no background job may disappear;
  - no job may start failing.

  A dry run of the new checks on the production copy passed.

## What the owner needs to do

- **Install the rollback helpers, once, from the version being deployed.** These are small programs the server
  runs with administrator rights so a deploy can keep the previous program, its service file and its settings file
  side by side. Without them there is no way back from this change, so the deploy command refuses to run, even
  with the usual "deploy anyway" override. Install them from a copy of the code that already includes this change,
  so the service file they carry uses the new names.
- **Rename the settings file to the new names, in the same deploy.** Twenty-eight old names need renaming. The
  current program only understands the old names, so rename them in the same deploy, not before. Most are a
  straight rename. A few need their value written differently:
  - yes/no settings must say `true` or `false`;
  - lists of addresses become a bracketed list.
  The deploy check names every old name it finds and the new name for each.
- **Take the usual disk snapshot just before the deploy,** and plan for about an hour of downtime.
- **After the deploy, back up the new key file,** as the earlier summary describes.

## What rollback means

- **What it is.** Rolling back puts the old program back and restores the safety copy of the database the new
  version took just before converting it. The rollback helpers also put the old settings file back, because the
  old program does not understand the new names.
- **What it costs.** Every change made after the conversion is lost: new books, edits, listening progress, and
  any password or key changed since. Get the owner's approval first.
- **Disk space.** The restore does not delete the database it replaces. It sets it aside as its own undo, about
  18 GB on the production copy, until it is pruned. Make sure the server's storage has that room before you roll
  back.
