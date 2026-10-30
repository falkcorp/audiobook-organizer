### Fixed

- Loosening a review filter no longer hides rows when "hide multi-book" is on. Groups of books sharing a candidate were counted after the minimum score, runtime, language and transcription filters, so loosening one let a second book join a group and hid the first (nothing listed at minimum score 150, 40 rows at 200). Groups are now counted before those thresholds, so a looser filter always lists a superset.
