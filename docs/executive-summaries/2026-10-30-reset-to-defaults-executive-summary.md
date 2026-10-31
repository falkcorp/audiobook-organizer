<!-- file: docs/executive-summaries/2026-10-30-reset-to-defaults-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e907faf-c6a6-4481-a43b-bac70a816483 -->
<!-- last-edited: 2026-10-30 -->

# Factory reset now restores every setting, not most of them

## Executive Summary

When you used Reset or Factory Reset, the app was meant to put every setting back to its
shipped value. It did so for most settings, but a hand-kept list of those values had quietly
fallen 131 entries behind the real defaults. For those 131, the reset wrote an empty or zero
value instead. That covered things like the search cache size, how many fingerprint workers
run at once, the default role for new sign-ins, the backup compression format, the Audiobookshelf
login lifetimes and the repeat interval of several scheduled jobs.

A zero in those places is not "default"; depending on the setting it could switch a feature
off or leave a scheduled job with no timer, and nothing said so.

The reset now reads the same list of shipped defaults the app uses at startup, so the two cannot
disagree again, and the hand-kept list is gone. A new check goes through every setting that has
a default and fails if a reset would leave it empty. As before, your library folder, database
location and settings that the host machine controls are kept.
