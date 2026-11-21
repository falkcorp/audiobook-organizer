<!-- file: docs/executive-summaries/2026-11-21-settings-move-to-core-executive-summary.md -->
<!-- version: 0.1.0 -->
<!-- guid: 2f9b7c14-6d3e-4a85-b0e2-91c4d8a5f367 -->
<!-- last-edited: 2026-11-21 -->

# Settings and saved passwords move into the new core

**Date:** 21 November 2026. The service's settings, and the passwords and API keys it stores, move out of the old
settings code and into the shared core that every part of the service now runs on. This is a one-way change to the
database, rehearsed on a copy of production before it ships.

## Executive Summary

- **What changes for the owner.**
  - The Settings page looks the same and saves the same values.
  - A saved password or API key is now shown only as "set" or "not set", with when it was set. It is never shown,
    not even partly masked, and it is stored sealed with a key file that lives in the service's private folder.
  - The server's settings file uses new, consistent names (all starting `ABO_`). The deploy command refuses a
    settings file that still uses an old name and says which name replaces it.
- **The first start of the new version converts the database. You cannot go back by swapping the program.**
  - The first start copies every setting into the new format, seals the stored passwords, and rewrites the whole
    store once in the new layout.
  - On a copy of production this took about 50 minutes, during which the service does not answer.
  - After that, the older program refuses to open the database rather than misread it.
  - Going back means restoring the safety copy the new version takes automatically before it changes anything.
- **A rollback problem was found and is being fixed before this ships.**
  - The rehearsal found that restoring that safety copy fails on the production server, because its private folder
    and its database sit on two different disks.
  - The restore refuses before it touches anything, so nothing is lost. But the way back has to work before the
    change ships, so a separate fix is in progress and the rehearsal will be repeated with it.
- **Schedules keep their timing.**
  - Maintenance jobs that ran in the nightly window, or only at startup, still do after the change.
  - The library scan's "run periodically" switch keeps working as before.
- **A fix that prevents losing every saved password.**
  - If the server lost power at the wrong moment just after creating the key file, the next start could trust a file
    that was not yet safely written. A second power loss could then leave every saved password unreadable, so
    each one would have to be typed in again.
  - The service now makes sure the key file is safely on disk before it seals anything with it, and the key-file
    backup command does the same before it copies the key.
- **A refused start now stops cleanly.**
  - When the service refuses to start because the database is newer than the program, or a conversion is blocked,
    it now exits with the code the system service manager treats as "do not restart". It stops instead of retrying
    in a loop.

## What the owner needs to do

- Before the deploy: update the server's settings file to the new names. `make deploy` lists any old name it finds.
- Take the usual disk snapshot just before the deploy.
- After the deploy: back up the new key file with the key-file export command (`secrets export-keyring`). Health shows
  a warning until you do. Keep that copy off the server: a backup taken after this change cannot be restored without it.
