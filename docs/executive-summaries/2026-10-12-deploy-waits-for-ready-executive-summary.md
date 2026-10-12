<!-- file: docs/executive-summaries/2026-10-12-deploy-waits-for-ready-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f233146-7c77-468b-ae45-0f0c243d1711 -->
<!-- last-edited: 2026-10-12 -->

# Deploys wait until the server is ready

Branch `task/07-r4`. Task brief:
[07-R4](../proposals/2026-10-holistic/tasks/07/07-R4.md).

## Executive Summary

- **A deploy no longer reports success too early.** After restarting the
  server, a deploy used to wait three seconds and then check the version.
  The server can need several minutes before it is fully up to speed, so a
  deploy could say "done" while the library was still slow or not yet
  answering. Deploys and rollbacks now wait, for up to five minutes, until
  the server itself says it is ready.
- **The server tells the operating system when it has started.** Once the
  server has passed its startup checks and is accepting connections, it now
  sends the standard "I'm up" signal that Linux service management
  understands. `systemctl restart` can then return only when the server can
  actually be reached.
- **A blocked port stops the start cleanly.** If something else already holds
  the server's port, startup now fails with a clear error. Before, the server
  could look as if it had started while serving nothing.
- **The owner installs the unit.** Using the new signal needs an updated
  service file on the server. Nothing in this change installs it; the owner
  copies it over when ready, after deploying a build that includes this change.

## What changes for the owner

- **Deploy and rollback output:** both now print how many seconds the server
  took to report ready before the version check runs.
- **Your own deploy settings file:** it is a private copy that is not part of
  the repository, so it does not change by itself. Copy the new wait step from
  the template to get it.
- **Rollback needs the server address:** rollback now needs the server's web
  address set in your deploy settings, so that it can wait for the restarted
  server too.
- **Installing the new service file:**
  1. Deploy a build from this change.
  2. Copy the service file into place.
  3. Reload the service manager.
  4. Restart the server.

  Do it in that order. An older build under the new service file never sends
  the "I'm up" signal, so it is stopped and restarted in a loop. The
  deployment guide (`docs/system/deploy-and-gpu-ops.md`) has the exact
  commands and the undo steps.
