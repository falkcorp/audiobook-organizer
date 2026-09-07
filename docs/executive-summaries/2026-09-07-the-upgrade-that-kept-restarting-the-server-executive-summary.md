<!-- file: docs/executive-summaries/2026-09-07-the-upgrade-that-kept-restarting-the-server-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 58d8b98b-ddc2-4c5c-80b0-a2beebfa66bb -->
<!-- last-edited: 2026-09-07 -->

# The upgrade that kept restarting the server

**Pull request:** #3088 (the fix) — this branch documents it.

## Executive Summary

- **What happened:** we moved the activity log (the running history of everything the
  app does) onto a new, faster storage engine. As part of that move, the app tries to
  copy the *existing* history from the old store into the new one, once, in the
  background. On the real server that one-time copy tried to load an entire slice of the
  history into memory at once. Memory ballooned to about 30 GB, the operating system's
  out-of-memory guard shot the app dead, the service restarted automatically — and then
  started the very same copy again. The result was a server that killed and restarted
  itself roughly every 14 minutes.

- **What you would have noticed:** the site going briefly unavailable and then "warming
  up" again, over and over, every few minutes. Nothing you did caused it and nothing you
  could do would stop it from the outside.

- **Was any data lost? No.** The new store was still in "copy and double-check" mode — the
  app kept reading and writing the old, trusted store the whole time and never switched
  over, because the switch only happens *after* the copy finishes and is verified, which it
  never did. The old history is intact. The only cost was the repeated restarts.

- **The immediate fix — turn the new engine back off:** there has always been meant to be
  a simple switch to fall back to the old, proven storage — no copy, no new engine, back to
  known-good behavior. When we went to flip that switch, we found **it was never actually
  wired up**: the setting existed on paper but no part of the app ever read it, so it did
  nothing no matter how it was set. That is the bug this PR fixes. With the switch now
  connected, we set it to "use the old engine" on the server, and the restart loop stopped
  immediately. The server has been stable since.

- **Why the switch was dead, in plain terms:** the app builds its settings by reading each
  one by name from a central list. This particular setting had been *declared* — it looked
  like a real, labeled setting — but the line that copies it out of the central list into
  the running configuration was missing. So it silently stayed empty forever, which happened
  to mean "use the new engine." A setting that looks real but that nothing reads is worse
  than no setting at all, because it looks like a working safety valve right up until you
  need it. We also added an automated test that fails if this switch ever stops working
  again, and made sure the setting survives the app reloading its configuration.

- **What's still to do (and what it means for you):** the new storage engine is **paused**,
  not abandoned. Before we turn it back on, we will rewrite that one-time history copy so it
  streams the old history through in small, bounded chunks instead of loading a whole slice
  at once — the memory problem, fixed at its root. Until then the app runs on the same
  proven storage it always has, so you should see no difference in day-to-day use.
