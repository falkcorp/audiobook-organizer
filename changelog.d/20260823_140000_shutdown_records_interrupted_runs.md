### Fixed

- An operation stopped by a server restart is now recorded as interrupted rather
  than as cancelled. Both cases previously wrote the same "cancelled" status, so
  nothing could tell work the server interrupted apart from work someone
  deliberately stopped. A run cancelled by a user, or killed by the stuck-operation
  watchdog, still records as cancelled — deliberately, so it can never be revived
  later. Nothing resumes automatically as a result of this change; it only corrects
  what is recorded.
