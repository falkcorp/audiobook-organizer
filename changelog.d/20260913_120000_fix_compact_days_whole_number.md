### Fixed

#### Activity Log "Custom days" compaction accepts only whole days from 1 to 36500

The Custom days box in the Activity Log Compact menu ran `parseInt`, so typing
`1.75` compacted everything older than one day. Compaction is irreversible (it
collapses entries into daily digests), so this could destroy detail the user
meant to keep. `0.5`, `-3` and `abc` did nothing and showed nothing. The box now
accepts only a whole number from 1 to 36500 and shows an inline error otherwise;
the two identical copies (mobile and desktop toolbars) are one shared
`CustomCompactDaysField` component.

A huge day count overflowed `time.Now().AddDate` and wrapped to a cutoff near
now (213503982334601 days) or tomorrow (int64 max), which compacted EVERYTHING.
`maintenance.MaxCompactDays` (36500) is now enforced in the UI, in
`POST /api/v1/activity/compact`, and in the `maintenance.compact-activity-log`
op itself, which also refuses any positive day count whose cutoff is not before
now.

`POST /api/v1/activity/compact` now distinguishes its 400s: a non-integer value
(`1.75`, `"3"`, `1e3`) gives "must be a whole number of days", a negative one
"must be zero or positive", an out-of-range one "must be between 0 and 36500",
and a missing or null `older_than_days` (which used to decode to 0, compact
everything) gives "is required". An explicit `0` still means everything up to
now.
