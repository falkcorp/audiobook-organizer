### Fixed

- **The activity-log migration could skip a daily-summary record and still report
  itself fully verified.** The migration now remembers its position so a restart
  does not start over — but a bookmark only works if nothing new ever appears
  behind it. The housekeeping job that condenses a day's entries into a daily
  summary writes only to the old store (by design, that is what the move is for),
  and it dates that summary to the day it covers rather than to the moment it was
  written. So housekeeping running while the migration was paused could leave a
  new record behind the bookmark, dated weeks earlier. The migration would step
  over it, and because it only re-checks records it just read, it would never
  notice — reporting the section as verified while missing a record that might
  itself be a correction to an older copy. Daily summaries are now always re-read
  in full rather than resumed. That is about one record per day of history; the
  several-million-record categories that made restarts expensive still resume.
