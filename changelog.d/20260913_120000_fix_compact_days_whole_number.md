### Fixed

#### Activity Log "Custom days" compaction accepts only whole days

The Custom days box in the Activity Log Compact menu ran `parseInt`, so typing
`1.75` compacted everything older than one day. Compaction is irreversible (it
collapses entries into daily digests), so this could destroy detail the user
meant to keep. `0.5`, `-3` and `abc` did nothing and showed nothing. The box now
accepts only a whole number of 1 or more and shows an inline error otherwise; the
two identical copies (mobile and desktop toolbars) are one shared
`CustomCompactDaysField` component. `POST /api/v1/activity/compact` now answers a
non-integer `older_than_days` with "must be a whole number of days" instead of
"must be zero or positive", and keeps `0` meaning everything up to now.
